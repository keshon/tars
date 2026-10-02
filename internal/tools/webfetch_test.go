package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/keshon/tars/internal/workspace"
)

func TestStripHTML_Basic(t *testing.T) {
	got := stripHTML(`<html><head><title>T</title><script>var x=1;</script></head><body><h1>Hi</h1><p>Text <a href="/x">link</a></p><!-- c --></body></html>`)
	if strings.Contains(got, "var x=1") || strings.Contains(got, "<h1>") || strings.Contains(got, "c -->") {
		t.Fatalf("tags/script/comment leaked: %q", got)
	}
	if !strings.Contains(got, "Hi") || !strings.Contains(got, "Text") || !strings.Contains(got, "link") {
		t.Fatalf("text lost: %q", got)
	}
}

func TestStripHTML_Entities(t *testing.T) {
	if got := stripHTML(`<p>a &amp; b &lt;c&gt;</p>`); !strings.Contains(got, "a & b <c>") {
		t.Fatalf("entities: %q", got)
	}
}

func TestWebfetch_HTML(t *testing.T) {
	webfetchTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return cannedResponse(r, "text/html",
			`<html><body><h1>Hello</h1><script>evil()</script><p>World</p></body></html>`)
	})
	defer func() { webfetchTransport = nil }()
	ws, _ := workspace.New(t.TempDir())
	out, err := wfRun(ws, `https://example.com/page`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.HasPrefix(out, "WEBFETCH") || !strings.Contains(out, "Hello") || !strings.Contains(out, "World") {
		t.Fatalf("out=%q", out)
	}
	if strings.Contains(out, "evil()") {
		t.Fatalf("script leaked: %q", out)
	}
}

func TestWebfetch_404IsError(t *testing.T) {
	webfetchTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return cannedResponseCode(r, http.StatusNotFound, "text/plain", "nope")
	})
	defer func() { webfetchTransport = nil }()
	ws, _ := workspace.New(t.TempDir())
	wf := Webfetch{WS: ws}
	if _, err := wf.Run(context.Background(), json.RawMessage(`{"url":"https://example.com/missing"}`)); err == nil {
		t.Fatal("expected error for 404")
	}
}

func TestWebfetch_LoopbackRefused(t *testing.T) {
	// No transport override: validation must reject before any dial.
	ws, _ := workspace.New(t.TempDir())
	wf := Webfetch{WS: ws}
	if _, err := wf.Run(context.Background(), json.RawMessage(`{"url":"http://127.0.0.1:3000/"}`)); err == nil {
		t.Fatal("expected SSRF refusal for loopback")
	}
}

func TestWebfetch_TruncatesWithSpill(t *testing.T) {
	big := "<p>" + strings.Repeat("x", 100*1024) + "</p>"
	webfetchTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return cannedResponse(r, "text/html", big)
	})
	defer func() { webfetchTransport = nil }()
	dir := t.TempDir()
	ws, _ := workspace.New(dir)
	out, err := Webfetch{WS: ws}.Run(context.Background(), json.RawMessage(`{"url":"https://example.com/big"}`))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "truncated") || !strings.Contains(out, "Full text:") {
		t.Fatalf("expected truncation + spill, got %.200q", out)
	}
}

// roundTripFunc serves canned responses for public-looking URLs that must
// validate but can never be dialed in a sandbox.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func cannedResponse(r *http.Request, contentType, body string) (*http.Response, error) {
	return cannedResponseCode(r, http.StatusOK, contentType, body)
}

func cannedResponseCode(r *http.Request, code int, contentType, body string) (*http.Response, error) {
	return &http.Response{
		Status:        http.StatusText(code),
		StatusCode:    code,
		Header:        http.Header{"Content-Type": {contentType}},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       r,
	}, nil
}

func wfRun(ws *workspace.Workspace, url string) (string, error) {
	return Webfetch{WS: ws}.Run(context.Background(), json.RawMessage(`{"url":`+quote(url)+`}`))
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
