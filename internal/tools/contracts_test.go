package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keshon/tars/internal/workspace"
)

func TestRedirectCannotReachLoopback(t *testing.T) {
	reached := false
	webfetchTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "example.com" {
			resp, _ := cannedResponseCode(r, 302, "text/plain", "")
			resp.Header.Set("Location", "http://127.0.0.1:43210/private")
			return resp, nil
		}
		reached = true
		return cannedResponse(r, "text/plain", "private-data")
	})
	defer func() { webfetchTransport = nil }()
	out, err := (Webfetch{}).Run(context.Background(), json.RawMessage(`{"url":"https://example.com"}`))
	if err == nil || reached || strings.Contains(out, "private-data") {
		t.Fatalf("repro failed: %v %s", err, out)
	}

}

func TestWebfetchRejectsNonIntegerLimit(t *testing.T) {
	_, err := (Webfetch{}).Run(context.Background(), json.RawMessage(`{"url":"https://example.com","max_bytes":"100"}`))
	if err == nil || !strings.Contains(err.Error(), "cannot unmarshal string") {
		t.Fatalf("repro failed: %v", err)
	}
	t.Log(err)
}

func TestPatchPreservesCRLFReplacement(t *testing.T) {
	dir := t.TempDir()
	ws, _ := workspace.New(dir)
	path := filepath.Join(dir, "x.txt")
	os.WriteFile(path, []byte("alpha\r\nbeta\r\n"), 0600)
	_, err := (PatchFile{WS: ws}).Run(context.Background(), json.RawMessage(`{"path":"x.txt","old_content":"beta","new_content":"new\r\nline"}`))
	body, _ := os.ReadFile(path)
	if err != nil || strings.Contains(string(body), "\r\r\n") || string(body) != "alpha\r\nnew\r\nline\r\n" {
		t.Fatalf("repro failed %q %v", body, err)
	}

}
