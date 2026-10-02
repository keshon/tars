package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHTTPMCP serves initialize, tools/list and tools/call. Mode selects
// the wire shape: "json" answers one object, "sse" answers an event
// stream. It records session headers and kills the session once when told
// (expireSession) to exercise the 404-renew path.
type fakeHTTPMCP struct {
	t             *testing.T
	mode          string
	mu            sync.Mutex
	sessionsSeen  []string
	expireOnce    bool
	expired       bool
	sessionIssued bool
}

func (f *fakeHTTPMCP) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		f.mu.Lock()
		got := r.Header.Get("Mcp-Session-Id")
		f.sessionsSeen = append(f.sessionsSeen, got)
		shouldExpire := f.expireOnce && !f.expired && req.Method != "initialize"
		if shouldExpire {
			f.expired = true
		}
		if !f.sessionIssued {
			f.sessionIssued = true
		}
		f.mu.Unlock()

		if shouldExpire {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Mcp-Session-Id", "sess-1")
		var result string
		switch req.Method {
		case "initialize":
			result = `{"serverInfo":{"name":"fakehttp","version":"0"}}`
		case "tools/list":
			result = `{"tools":[
				{"name":"echo","description":"echo back","inputSchema":{"type":"object"}},
				{"name":"echo","description":"duplicate name","inputSchema":{"type":"object"}},
				{"name":"` + strings.Repeat("x", 100) + `","description":"long name","inputSchema":{"type":"object"}}
			]}`
		case "tools/call":
			result = `{"content":[{"type":"text","text":"http says hi"}]}`
		default:
			result = `{}`
		}
		msg, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": json.RawMessage(result)})
		if f.mode == "sse" {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, ": progress ping\n\ndata: %s\n\n", msg)
		} else {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(msg)
		}
	}
}

func TestMCPHTTP_HandshakeAndCall(t *testing.T) {
	for _, mode := range []string{"json", "sse"} {
		t.Run(mode, func(t *testing.T) {
			fake := &fakeHTTPMCP{t: t, mode: mode}
			srv := httptest.NewServer(fake.handler())
			defer srv.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			tools, client, err := StartMCPHTTP(ctx, "web", srv.URL)
			if err != nil {
				t.Fatalf("start: %v", err)
			}
			defer client.Close()
			// Duplicate "echo" dedups to echo/_2; the 100-char name is
			// capped at 64. Three defs, three distinct short names.
			if len(tools) != 3 {
				t.Fatalf("tools=%d, want 3", len(tools))
			}
			seen := map[string]bool{}
			for _, tl := range tools {
				if seen[tl.Name()] {
					t.Fatalf("duplicate exposed name %q", tl.Name())
				}
				seen[tl.Name()] = true
				if len(tl.Name()) > mcpMaxToolName {
					t.Fatalf("name too long: %q", tl.Name())
				}
			}
			if tools[0].Name() != "mcp__web__echo" || tools[1].Name() != "mcp__web__echo_2" {
				t.Fatalf("dedup wrong: %q %q", tools[0].Name(), tools[1].Name())
			}
			out, err := tools[0].Run(ctx, json.RawMessage(`{}`))
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if !strings.Contains(out, "http says hi") {
				t.Fatalf("out=%q", out)
			}
			fake.mu.Lock()
			defer fake.mu.Unlock()
			sent := false
			for _, s := range fake.sessionsSeen {
				if s == "sess-1" {
					sent = true
				}
			}
			if !sent {
				t.Fatalf("session id never sent back: %v", fake.sessionsSeen)
			}
		})
	}
}

func TestMCPHTTP_SessionExpiryRenews(t *testing.T) {
	fake := &fakeHTTPMCP{t: t, mode: "json", expireOnce: true}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tools, client, err := StartMCPHTTP(ctx, "web", srv.URL)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer client.Close()
	// tools/list was answered 404 once (expired), renewed, and succeeded.
	if len(tools) != 3 {
		t.Fatalf("tools=%d, want 3 after renew", len(tools))
	}
}

func TestMCPHTTP_LoopbackAllowedButPublicEnforced(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := StartMCPHTTP(ctx, "x", "http://10.0.0.5/mcp"); err == nil {
		t.Fatal("expected refusal for private IP")
	}
	if _, _, err := StartMCPHTTP(ctx, "x", "ftp://example.com/mcp"); err == nil {
		t.Fatal("expected refusal for non-http scheme")
	}
}

func TestIsMCPURL(t *testing.T) {
	// Input is post-ParseMCPFlag commands (the name= prefix is already
	// stripped by the time discovery routes on this).
	if !IsMCPURL("https://example.com/mcp") {
		t.Error("bare https URL not detected")
	}
	if !IsMCPURL("http://127.0.0.1:8000/mcp") {
		t.Error("loopback http URL not detected")
	}
	if IsMCPURL("node server.js") || IsMCPURL("python mcp.py") {
		t.Error("command misdetected as URL")
	}
}
