package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keshon/tars/internal/workspace"
)

func previewWS(t *testing.T) (*workspace.Workspace, string) {
	t.Helper()
	dir := t.TempDir()
	ws, err := workspace.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	return ws, dir
}

func TestPreviewPatch_ShowsDiff(t *testing.T) {
	ws, dir := previewWS(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\nthree\nfour\nfive\nsix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := PreviewArgs(ws, "patch_file", json.RawMessage(`{"path":"a.txt","old_content":"three","new_content":"THREE"}`))
	if !strings.Contains(got, "-three") || !strings.Contains(got, "+THREE") {
		t.Fatalf("got %q", got)
	}
}

func TestPreviewPatch_MissingOldContent(t *testing.T) {
	ws, dir := previewWS(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := PreviewArgs(ws, "patch_file", json.RawMessage(`{"path":"a.txt","old_content":"nope","new_content":"x"}`))
	if !strings.Contains(got, "would fail") {
		t.Fatalf("got %q", got)
	}
}

func TestPreviewWrite_NewAndIdentical(t *testing.T) {
	ws, _ := previewWS(t)
	got := PreviewArgs(ws, "write_file", json.RawMessage(`{"path":"n.txt","content":"a\nb\n"}`))
	if !strings.Contains(got, "new file") {
		t.Fatalf("got %q", got)
	}
}

func TestPreviewMove(t *testing.T) {
	ws, _ := previewWS(t)
	got := PreviewArgs(ws, "move_file", json.RawMessage(`{"from":"a.txt","to":"b.txt"}`))
	if got != "rename a.txt -> b.txt" {
		t.Fatalf("got %q", got)
	}
}

func TestPreview_NonMutatingEmpty(t *testing.T) {
	ws, _ := previewWS(t)
	for _, tool := range []string{"read_file", "run_shell", "grep_files", "unknown_tool"} {
		if got := PreviewArgs(ws, tool, json.RawMessage(`{}`)); got != "" {
			t.Fatalf("%s: got %q, want empty", tool, got)
		}
	}
	if got := PreviewArgs(ws, "patch_file", json.RawMessage(`not json`)); got != "" {
		t.Fatalf("bad json: got %q", got)
	}
}

func TestPreview_Capped(t *testing.T) {
	ws, dir := previewWS(t)
	var old []string
	for i := 0; i < 200; i++ {
		old = append(old, "line")
	}
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(strings.Join(old, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	big, _ := json.Marshal(map[string]string{
		"path": "big.txt", "old_content": "line", "new_content": "LINE",
	})
	// Ambiguous (200 matches) reports failure, not a 200-line diff.
	if got := PreviewArgs(ws, "patch_file", big); !strings.Contains(got, "would fail") {
		t.Fatalf("got %.100q", got)
	}
}
