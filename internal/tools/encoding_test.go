package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keshon/tars/internal/workspace"
)

// utf16le encodes s as BOM-prefixed UTF-16LE with CRLF line endings —
// the shape PowerShell 5's Out-File and many older Windows editors write.
func utf16le(s string) []byte {
	out := []byte{0xFF, 0xFE}
	for _, r := range s {
		out = append(out, byte(r), byte(r>>8))
	}
	return out
}

func TestDecodeText_UTF16LE(t *testing.T) {
	text, enc, binary := decodeText(utf16le("world\r\n"))
	if enc != "utf-16le" || binary {
		t.Fatalf("enc=%q binary=%v", enc, binary)
	}
	if text != "world\n" {
		t.Fatalf("text=%q", text)
	}
}

func TestReadFile_UTF16ReadableWithEncodingHeader(t *testing.T) {
	dir := t.TempDir()
	ws, _ := workspace.New(dir)
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), utf16le("world\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := ReadFile{WS: ws}.Run(context.Background(), json.RawMessage(`{"path":"b.txt"}`))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "encoding: utf-16le") {
		t.Fatalf("no encoding header: %q", out)
	}
	if !strings.Contains(out, "world") || strings.Contains(out, "binary: true") {
		t.Fatalf("not decoded: %q", out)
	}
}

func TestPatchFile_RefusesUTF16WithReason(t *testing.T) {
	dir := t.TempDir()
	ws, _ := workspace.New(dir)
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), utf16le("world\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := PatchFile{WS: ws}.Run(context.Background(),
		json.RawMessage(`{"path":"b.txt","old_content":"world","new_content":"WORLD"}`))
	if err == nil {
		t.Fatal("expected refusal")
	}
	// The failure must name the cause and the fix, not just "not found":
	// a live run circled for seven steps because it never learned this.
	if !strings.Contains(err.Error(), "utf-16le") || !strings.Contains(err.Error(), "Convert it first") {
		t.Fatalf("unhelpful error: %v", err)
	}
	if strings.Contains(err.Error(), "not found") {
		t.Fatalf("must not degrade to not-found: %v", err)
	}
}

func TestPatchLines_RefusesUTF16(t *testing.T) {
	dir := t.TempDir()
	ws, _ := workspace.New(dir)
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), utf16le("a\r\nb\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pl := PatchLines{WS: ws}
	if _, err := pl.Run(context.Background(),
		json.RawMessage(`{"path":"b.txt","start_line":1,"end_line":1,"new_content":"A"}`)); err == nil {
		t.Fatal("expected refusal")
	}
}
