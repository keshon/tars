package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keshon/tars/internal/workspace"
)

func TestReadFile_TruncatesLargeFiles(t *testing.T) {
	dir := t.TempDir()
	ws, err := workspace.New(dir)
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}

	large := strings.Repeat("x", 200*1024)
	path := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(path, []byte(large), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	args, _ := json.Marshal(map[string]string{"path": "big.txt"})
	out, err := ReadFile{WS: ws}.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "...(content truncated at ") {
		t.Fatalf("expected truncation marker, got len %d", len(out))
	}
	if !strings.Contains(out, "FILE\n") || !strings.Contains(out, "truncated: true") || !strings.Contains(out, "size: ") {
		t.Fatalf("expected structured FILE header, got: %q", out[:minLen(200, len(out))])
	}
	if len(out) >= len(large) {
		t.Fatalf("output not truncated: len %d", len(out))
	}
}

func TestReadFile_SmallFileUnchanged(t *testing.T) {
	dir := t.TempDir()
	ws, err := workspace.New(dir)
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}

	content := "hello world"
	if err := os.WriteFile(filepath.Join(dir, "small.txt"), []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	args, _ := json.Marshal(map[string]string{"path": "small.txt"})
	out, err := ReadFile{WS: ws}.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "FILE\n") || !strings.Contains(out, "truncated: false") {
		t.Fatalf("expected structured FILE header, got: %q", out)
	}
	if !strings.Contains(out, "hello world") {
		t.Fatalf("expected file content in output, got: %q", out)
	}
}

func minLen(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestReadFile_MetadataOnly_NoBody(t *testing.T) {
	dir := t.TempDir()
	ws, err := workspace.New(dir)
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}

	large := strings.Repeat("x", 200*1024)
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(large), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	args, _ := json.Marshal(map[string]any{"path": "big.txt", "metadata_only": true})
	out, err := ReadFile{WS: ws}.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(out, "----") {
		t.Fatalf("metadata_only should not include body separator, got len %d", len(out))
	}
	if !strings.Contains(out, "size: 204800") || !strings.Contains(out, "truncated: true") {
		t.Fatalf("expected size/truncated facts in header, got: %q", out)
	}
	if len(out) > 200 {
		t.Fatalf("metadata_only output too large: %d bytes", len(out))
	}
}

// max_bytes=0 must mean "no explicit cap" — every model reads 0 as the
// universal "unlimited" convention. The original 0-means-header-only
// semantics sent a live Qwen run into an 18-repeat read loop: it kept
// asking for content with max_bytes=0 and kept getting a contentless
// header it couldn't understand.
func TestReadFile_MaxBytesZero_ReturnsFullContent(t *testing.T) {
	dir := t.TempDir()
	ws, _ := workspace.New(dir)
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hello"), 0o644)

	zero := 0
	args, _ := json.Marshal(map[string]any{"path": "f.txt", "max_bytes": zero})
	out, err := ReadFile{WS: ws}.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("max_bytes=0 must return the full content, got: %q", out)
	}
}

func TestReadFile_MaxBytesPositive_CapsContent(t *testing.T) {
	dir := t.TempDir()
	ws, _ := workspace.New(dir)
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hello world"), 0o644)

	args, _ := json.Marshal(map[string]any{"path": "f.txt", "max_bytes": 5})
	out, err := ReadFile{WS: ws}.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "hello") || strings.Contains(out, "world") {
		t.Fatalf("max_bytes=5 should cap at 5 bytes, got: %q", out)
	}
}

// A single tool result must not be able to consume most of a small
// context window. Live (2026-09-05): reading a 205KB fixture returned
// the then-current 128KB cap, which took the prompt from 3.4k to 57k
// tokens — 87% of a 65k window — in one step, and the run timed out.
func TestReadFile_CapIsSmallEnoughForASmallContext(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("abcdefgh", 40*1024) // 320KB
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(big), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ws, err := workspace.New(dir)
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}

	args, _ := json.Marshal(map[string]string{"path": "big.txt"})
	out, err := (ReadFile{WS: ws}).Run(context.Background(), args)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(out) > readMaxBytes+1024 { // body plus the FILE header
		t.Errorf("read returned %d bytes, cap is %d", len(out), readMaxBytes)
	}
	if !strings.Contains(out, "truncated: true") {
		t.Error("a truncated read must say so")
	}
	if !strings.Contains(out, "size: 327680") {
		t.Error("a truncated read must still report the file's real size")
	}
}

// Live (probe 05): the agent rewrote a whole file to change one constant,
// which the system prompt had always told it not to do. Rewriting drops
// everything the model does not happen to reproduce, and nothing reports
// it — the surrounding code is simply gone.
func TestWriteFile_RefusesARewriteThatIsReallyAnEdit(t *testing.T) {
	dir := t.TempDir()
	original := "package sample\n\nconst Version = \"v1\"\n\nfunc Greet() string {\n\treturn \"hello\"\n}\n\nfunc Bye() string {\n\treturn \"bye\"\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "sample.go"), []byte(original), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ws, err := workspace.New(dir)
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}

	edited := strings.Replace(original, `"v1"`, `"v2"`, 1)
	args, _ := json.Marshal(map[string]string{"path": "sample.go", "content": edited})
	if _, err := (WriteFile{WS: ws}).Run(context.Background(), args); err == nil {
		t.Fatal("a one-line change delivered as a whole-file rewrite was accepted")
	}

	// The file must be untouched: a refusal that already wrote is no
	// refusal at all.
	after, _ := os.ReadFile(filepath.Join(dir, "sample.go"))
	if string(after) != original {
		t.Error("the refused write modified the file anyway")
	}
}

func TestWriteFile_AllowsGenuineRewritesAndNewFiles(t *testing.T) {
	dir := t.TempDir()
	ws, err := workspace.New(dir)
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	w := WriteFile{WS: ws}

	// A new file is never a rewrite.
	args, _ := json.Marshal(map[string]string{
		"path": "new.go", "content": "package a\n\nfunc A() {}\n"})
	if _, err := w.Run(context.Background(), args); err != nil {
		t.Fatalf("new file refused: %v", err)
	}

	// Replacing a file with genuinely different content is the case
	// write_file exists for.
	long := "package a\n"
	for i := 0; i < 20; i++ {
		long += fmt.Sprintf("// original line %d\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, "big.go"), []byte(long), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	replacement := "package b\n"
	for i := 0; i < 20; i++ {
		replacement += fmt.Sprintf("// completely different %d\n", i)
	}
	args, _ = json.Marshal(map[string]string{"path": "big.go", "content": replacement})
	if _, err := w.Run(context.Background(), args); err != nil {
		t.Fatalf("genuine rewrite refused: %v", err)
	}
}

func TestWriteFile_OversizedContentRefused(t *testing.T) {
	dir := t.TempDir()
	ws, err := workspace.New(dir)
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	big := strings.Repeat("x", writeMaxBytes+1)
	args, _ := json.Marshal(map[string]string{"path": "big.go", "content": big})
	if _, err := (WriteFile{WS: ws}).Run(context.Background(), args); err == nil ||
		!strings.Contains(err.Error(), "write cap") {
		t.Fatalf("expected write-cap refusal, got %v", err)
	}
	if _, stat := os.Stat(filepath.Join(dir, "big.go")); !os.IsNotExist(stat) {
		t.Fatal("refused write must not touch disk")
	}
}
