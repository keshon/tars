package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><h1>Hello</h1><script>evil()</script><p>World</p></body></html>`))
	}))
	defer srv.Close()
	ws, _ := workspace.New(t.TempDir())
	out, err := Webfetch{WS: ws}.Run(context.Background(), json.RawMessage(`{"url":`+quote(srv.URL)+`}`))
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	ws, _ := workspace.New(t.TempDir())
	wf := Webfetch{WS: ws}
	if _, err := wf.Run(context.Background(), json.RawMessage(`{"url":`+quote(srv.URL)+`}`)); err == nil {
		t.Fatal("expected error for 404")
	}
}

func TestWebfetch_TruncatesWithSpill(t *testing.T) {
	big := "<p>" + strings.Repeat("x", 100*1024) + "</p>"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(big))
	}))
	defer srv.Close()
	dir := t.TempDir()
	ws, _ := workspace.New(dir)
	out, err := Webfetch{WS: ws}.Run(context.Background(), json.RawMessage(`{"url":`+quote(srv.URL)+`}`))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "truncated") || !strings.Contains(out, "Full text:") {
		t.Fatalf("expected truncation + spill, got %.200q", out)
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
