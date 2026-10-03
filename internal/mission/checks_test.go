package mission

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keshon/tars/internal/workspace"
)

func testWS(t *testing.T) *workspace.Workspace {
	t.Helper()
	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	return ws
}

func TestRunCheck_None(t *testing.T) {
	out, ok := RunCheck(context.Background(), Check{Type: "none"}, testWS(t))
	if !ok {
		t.Fatalf("none check should pass, got: %s", out)
	}
	if _, ok := RunCheck(context.Background(), Check{}, testWS(t)); !ok {
		t.Fatal("empty type should behave like none")
	}
}

func TestRunCheck_FileAbsent(t *testing.T) {
	ws := testWS(t)
	ctx := context.Background()

	if out, ok := RunCheck(ctx, Check{Type: "file_absent", Path: "gone.txt"}, ws); !ok {
		t.Fatalf("absent file should pass, got: %s", out)
	}
	if out, ok := RunCheck(ctx, Check{Type: "file_absent"}, ws); ok {
		t.Fatalf("empty path should fail, got: %s", out)
	}

	full := filepath.Join(ws.Root(), "gone.txt")
	if err := os.WriteFile(full, []byte("still here"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, ok := RunCheck(ctx, Check{Type: "file_absent", Path: "gone.txt"}, ws); ok {
		t.Fatalf("existing file should fail, got: %s", out)
	}
	if got := (Check{Type: "file_absent", Path: "gone.txt"}).Render(); !strings.Contains(got, "absent") {
		t.Fatalf("render = %q", got)
	}
}

func TestRunCheck_FileExists(t *testing.T) {
	ws := testWS(t)
	ctx := context.Background()

	if out, ok := RunCheck(ctx, Check{Type: "file_exists", Path: "a.txt"}, ws); ok {
		t.Fatalf("missing file should fail, got: %s", out)
	}

	full := filepath.Join(ws.Root(), "a.txt")
	if err := os.WriteFile(full, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, ok := RunCheck(ctx, Check{Type: "file_exists", Path: "a.txt"}, ws); ok {
		t.Fatalf("empty file should fail — existence alone proves nothing, got: %s", out)
	}

	if err := os.WriteFile(full, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, ok := RunCheck(ctx, Check{Type: "file_exists", Path: "a.txt"}, ws); !ok {
		t.Fatalf("non-empty file should pass, got: %s", out)
	}

	if out, ok := RunCheck(ctx, Check{Type: "file_exists", Path: "../escape.txt"}, ws); ok {
		t.Fatalf("path escaping the workspace must fail, got: %s", out)
	}

	if err := os.Mkdir(filepath.Join(ws.Root(), "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, ok := RunCheck(ctx, Check{Type: "file_exists", Path: "sub"}, ws); ok {
		t.Fatalf("directory should not satisfy a file check, got: %s", out)
	}
}

func TestRunCheck_Shell(t *testing.T) {
	ws := testWS(t)
	ctx := context.Background()

	// echo works under both cmd.exe and sh.
	out, ok := RunCheck(ctx, Check{Type: "shell", Cmd: "echo hello-check"}, ws)
	if !ok {
		t.Fatalf("echo should pass, got: %s", out)
	}
	if !strings.Contains(out, "hello-check") {
		t.Fatalf("output should carry real command output, got: %s", out)
	}

	if out, ok := RunCheck(ctx, Check{Type: "shell", Cmd: "exit 1"}, ws); ok {
		t.Fatalf("failing command should fail the check, got: %s", out)
	}
}

func TestRunCheck_HTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ok" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	ws := testWS(t)
	ctx := context.Background()

	if out, ok := RunCheck(ctx, Check{Type: "http", URL: srv.URL + "/ok"}, ws); !ok {
		t.Fatalf("200 should pass, got: %s", out)
	}
	if out, ok := RunCheck(ctx, Check{Type: "http", URL: srv.URL + "/missing"}, ws); ok {
		t.Fatalf("404 should fail, got: %s", out)
	}
	if out, ok := RunCheck(ctx, Check{Type: "http", URL: "http://127.0.0.1:1"}, ws); ok {
		t.Fatalf("connection refusal should fail, got: %s", out)
	}
}

func TestRunCheck_ContentContains(t *testing.T) {
	ws := testWS(t)
	ctx := context.Background()
	full := filepath.Join(ws.Root(), "game.js")

	if out, ok := RunCheck(ctx, Check{Type: "content_contains", Path: "game.js", Contains: "raycast"}, ws); ok {
		t.Fatalf("missing file should fail, got: %s", out)
	}

	if err := os.WriteFile(full, []byte("function draw() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, ok := RunCheck(ctx, Check{Type: "content_contains", Path: "game.js", Contains: "raycast"}, ws); ok {
		t.Fatalf("wrong content should fail, got: %s", out)
	}
	if out, ok := RunCheck(ctx, Check{Type: "content_contains", Path: "game.js", Contains: "draw"}, ws); !ok {
		t.Fatalf("matching content should pass, got: %s", out)
	}
	if out, ok := RunCheck(ctx, Check{Type: "content_contains", Path: "game.js", Contains: "  "}, ws); ok {
		t.Fatalf("blank needle should fail, got: %s", out)
	}
}

func TestValidateCheck_Types(t *testing.T) {
	good := []Check{
		{},
		{Type: "none"},
		{Type: "file_exists", Path: "a"},
		{Type: "file_absent", Path: "a"},
		{Type: "content_contains", Path: "a", Contains: "b"},
		{Type: "shell", Cmd: "go test ./..."},
		{Type: "http", URL: "http://x/"},
	}
	for _, c := range good {
		if err := ValidateCheck(c); err != nil {
			t.Errorf("ValidateCheck(%+v) = %v, want nil", c, err)
		}
	}
	bad := []Check{
		{Type: "content_containz", Path: "a", Contains: "b"},
		{Type: "file_exists"},
		{Type: "content_contains", Path: "a"},
		{Type: "content_contains", Path: "a", Contains: "  "},
		{Type: "shell"},
		{Type: "http"},
	}
	for _, c := range bad {
		if err := ValidateCheck(c); err == nil {
			t.Errorf("ValidateCheck(%+v) = nil, want error", c)
		}
	}
}

func TestDerivedChecks_SkipsWhatTheDeclaredCheckAlreadyCovers(t *testing.T) {
	sub := &Subtask{
		FilesHint: []string{"index.html", "style.css"},
		Check:     Check{Type: "content_contains", Path: "index.html", Contains: "<button"},
	}
	got := DerivedChecks(sub)
	if len(got) != 1 || got[0].Path != "style.css" {
		t.Fatalf("DerivedChecks = %+v, want one file_exists for style.css only", got)
	}
}

func TestDerivedChecks_NothingToDeriveIsNotAFailure(t *testing.T) {
	sub := &Subtask{FilesHint: nil, Check: Check{Type: "none"}}
	if got := DerivedChecks(sub); len(got) != 0 {
		t.Fatalf("DerivedChecks = %+v, want none", got)
	}
}
