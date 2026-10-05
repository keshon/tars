package mission

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildMap_TreeHistogramAndManifests(t *testing.T) {
	ws := testWS(t)
	root := ws.Root()
	files := map[string]string{
		"go.mod":              "module example\n\ngo 1.22\n",
		"README.md":           "# Example\nA test project.\n",
		"main.go":             "package main\n",
		"src/game.js":         "loop()",
		"src/util.js":         "x",
		"node_modules/x/y.js": "junk",
		".tars/tasks/a/x.txt": "junk",
	}
	for p, content := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	m := BuildMap(ws)

	for _, want := range []string{
		"## Directory tree",
		"(root) — 3 files",
		"src — 2 files",
		"## File types",
		".js ×2",
		"## go.mod (head)",
		"module example",
		"## README.md (head)",
		"# Example",
	} {
		if !strings.Contains(m, want) {
			t.Fatalf("map missing %q:\n%s", want, m)
		}
	}
	for _, banned := range []string{"node_modules", ".tars"} {
		if strings.Contains(m, banned) {
			t.Fatalf("map leaked %s:\n%s", banned, m)
		}
	}
}

func TestBuildMap_EmptyWorkspace(t *testing.T) {
	if m := BuildMap(testWS(t)); m != "" {
		t.Fatalf("empty workspace map = %q, want empty", m)
	}
}

func TestBuildMap_RespectsBudget(t *testing.T) {
	ws := testWS(t)
	for i := 0; i < 120; i++ {
		dir := filepath.Join(ws.Root(), "pkg", strings.Repeat("x", 40)+string(rune('a'+i%26))+string(rune('a'+i/26)))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "f.go"), []byte("package x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := BuildMap(ws)
	if len(m) > mapBudgetChars+64 {
		t.Fatalf("map is %d chars, budget is %d", len(m), mapBudgetChars)
	}
	if !strings.Contains(m, "more directories not shown") && !strings.Contains(m, "map truncated") {
		t.Fatalf("oversized map must carry a truncation marker:\n%s", m[:200])
	}
}

func TestReadHead_SkipsBinary(t *testing.T) {
	ws := testWS(t)
	bin := filepath.Join(ws.Root(), "index.html") // manifest name, binary content
	if err := os.WriteFile(bin, []byte{0x00, 0x01, 0x02}, 0o644); err != nil {
		t.Fatal(err)
	}
	if head := readHead(bin); head != "" {
		t.Fatalf("binary head = %q, want empty", head)
	}
}
