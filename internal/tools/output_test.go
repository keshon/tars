package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSpill_WritesFullContent(t *testing.T) {
	root := t.TempDir()
	content := strings.Repeat("x", 100)
	p := Spill(root, "run_shell", content)
	if p == "" {
		t.Fatal("expected path, got empty")
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read spill: %v", err)
	}
	if string(data) != content {
		t.Fatal("spill content mismatch")
	}
	if !strings.HasPrefix(p, filepath.Join(root, ".agent", "output")) {
		t.Fatalf("spill outside .agent/output: %s", p)
	}
}

func TestSpill_EmptyInputs(t *testing.T) {
	if p := Spill("", "run_shell", "x"); p != "" {
		t.Fatalf("expected empty, got %s", p)
	}
	if p := Spill(t.TempDir(), "run_shell", ""); p != "" {
		t.Fatalf("expected empty, got %s", p)
	}
}
