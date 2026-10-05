package workspace

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskDir_NamespacesTasks(t *testing.T) {
	if got := TaskDir("abc123"); got != filepath.Join(".tars", "tasks", "abc123") {
		t.Fatalf("TaskDir = %q", got)
	}
}

func TestTitleLine_ShapesText(t *testing.T) {
	if got := TitleLine("fix the bug\nsecond line"); got != "fix the bug" {
		t.Fatalf("TitleLine = %q", got)
	}
	if got := TitleLine("  spaced  "); got != "spaced" {
		t.Fatalf("TitleLine = %q", got)
	}
	long := strings.Repeat("x", 100)
	if got := TitleLine(long); len([]rune(got)) != 60 {
		t.Fatalf("TitleLine runes = %d, want 60", len([]rune(got)))
	}
}

func TestSessionTitle_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	if got := ReadSessionTitle(dir); got != "" {
		t.Fatalf("ReadSessionTitle = %q, want empty", got)
	}
	if err := WriteSessionTitle(dir, "my session"); err != nil {
		t.Fatal(err)
	}
	if got := ReadSessionTitle(dir); got != "my session" {
		t.Fatalf("ReadSessionTitle = %q", got)
	}
	if err := WriteSessionTitle(dir, ""); err != nil {
		t.Fatal(err)
	}
	if got := ReadSessionTitle(dir); got != "" {
		t.Fatalf("ReadSessionTitle after clear = %q", got)
	}
}
