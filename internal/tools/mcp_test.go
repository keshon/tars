package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

// fakeMCPServer answers initialize, tools/list and tools/call with canned
// responses. Runs in-process over pipes via connectMCP.
func fakeMCPServer(t *testing.T, r io.Reader, w io.Writer) {
	t.Helper()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	bw := bufio.NewWriter(w)
	respond := func(id int, result string) {
		msg, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": id, "result": json.RawMessage(result),
		})
		msg = append(msg, '\n')
		_, _ = bw.Write(msg)
		_ = bw.Flush()
	}
	for sc.Scan() {
		var req struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			continue
		}
		if req.Method == "notifications/initialized" {
			continue
		}
		switch req.Method {
		case "initialize":
			respond(req.ID, `{"serverInfo":{"name":"fake","version":"0"}}`)
		case "tools/list":
			respond(req.ID, `{"tools":[
				{"name":"echo","description":"echo back","inputSchema":{"type":"object"}},
				{"name":"boom","description":"always errors","inputSchema":{"type":"object"}}
			]}`)
		case "tools/call":
			var p struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if p.Name == "boom" {
				respond(req.ID, `{"content":[{"type":"text","text":"kaput"}],"isError":true}`)
			} else {
				respond(req.ID, `{"content":[{"type":"text","text":"hello from fake"}]}`)
			}
		}
	}
}

func TestMCPDiscoverAndCall(t *testing.T) {
	clientR, serverW := io.Pipe()
	serverR, clientW := io.Pipe()
	go fakeMCPServer(t, serverR, serverW)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tools, client, err := connectMCP(ctx, "fake", nil, clientW, clientR)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()
	if len(tools) != 2 {
		t.Fatalf("tools=%d, want 2", len(tools))
	}
	if tools[0].Name() != "mcp__fake__echo" {
		t.Fatalf("name=%q", tools[0].Name())
	}
	out, err := tools[0].Run(ctx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "hello from fake") {
		t.Fatalf("out=%q", out)
	}
	if _, err := tools[1].Run(ctx, json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected isError to surface as error")
	}
}

func TestMCPParseFlag(t *testing.T) {
	names, cmds, args := ParseMCPFlag("fs=node mcp-fs.js /root;git=python mcp-git.py")
	if len(names) != 2 || names[0] != "fs" || names[1] != "git" {
		t.Fatalf("names=%v", names)
	}
	if cmds[0] != "node" || len(args[0]) != 2 || args[0][1] != "/root" {
		t.Fatalf("cmds=%v args=%v", cmds, args)
	}
	if _, _, _ = ParseMCPFlag(""); true {
		n, _, _ := ParseMCPFlag(";;;")
		if len(n) != 0 {
			t.Fatalf("expected empty, got %v", n)
		}
	}
}

func TestMCPAllowedMime(t *testing.T) {
	if !allowedMCPMime("text/plain") || !allowedMCPMime("application/json") {
		t.Error("text/json must pass")
	}
	if allowedMCPMime("image/png") || allowedMCPMime("application/octet-stream") {
		t.Error("binary must not pass")
	}
}
