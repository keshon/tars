package mission

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceListing_SkipsNoiseAndReportsRealFiles(t *testing.T) {
	ws := testWS(t)
	root := ws.Root()
	for _, p := range []string{
		"index.html",
		filepath.Join("src", "game.js"),
		filepath.Join("node_modules", "lib", "junk.js"),
		filepath.Join(".git", "HEAD"),
		filepath.Join(".tars", "tasks", "x", "state.json"),
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	listing, existing := WorkspaceListing(ws)
	if !strings.Contains(listing, "index.html") || !strings.Contains(listing, "src/game.js") {
		t.Fatalf("listing missing real files:\n%s", listing)
	}
	for _, banned := range []string{"node_modules", ".git", ".tars"} {
		if strings.Contains(listing, banned) {
			t.Fatalf("listing leaked %s:\n%s", banned, listing)
		}
	}
	if !existing["index.html"] || !existing["src/game.js"] {
		t.Fatalf("existing set incomplete: %v", existing)
	}
	if existing["node_modules/lib/junk.js"] {
		t.Fatal("existing set should not include skipped dirs")
	}
}

func TestWorkspaceListing_EmptyWorkspace(t *testing.T) {
	listing, existing := WorkspaceListing(testWS(t))
	if !strings.Contains(listing, "empty") {
		t.Fatalf("empty workspace should say so, got: %s", listing)
	}
	if len(existing) != 0 {
		t.Fatalf("existing = %v, want empty", existing)
	}
}

func TestCompileSeed_CarriesTaskLedgerAndFacts(t *testing.T) {
	m := sampleMission()
	m.Mutated = []string{"index.html"}
	sub := &m.Subtasks[1] // s2, the NOW subtask

	seed := CompileSeed(m, sub, "index.html\ngame.js")

	for _, want := range []string{
		"build a small site",                             // mission task, verbatim
		"s1 [done] create html shell",                    // ledger reinjection
		"wrote: index.html; check: PASSED",               // measured facts ride along
		"s2 [NOW] game loop",                             // cursor marker
		"Your subtask (s2: game loop)",                   // identity
		"Add game.js with a requestAnimationFrame loop.", // goal verbatim
		"- game.js exists",                               // acceptance bullets
		"- loop draws each frame",
		"game.js, index.html",      // files_hint ∪ mission-wide mutated
		"file must exist: game.js", // rendered check
		"index.html\ngame.js",      // workspace listing
	} {
		if !strings.Contains(seed, want) {
			t.Fatalf("seed missing %q:\n%s", want, seed)
		}
	}
}

func TestCompileSeed_NoAcceptanceNoFiles(t *testing.T) {
	m := &Mission{Task: "t", Subtasks: []Subtask{{
		ID: "s1", Title: "x", Goal: "g", Check: Check{Type: "none"},
	}}}
	seed := CompileSeed(m, &m.Subtasks[0], "(workspace is empty)")
	if !strings.Contains(seed, "(none declared)") {
		t.Fatalf("missing acceptance placeholder:\n%s", seed)
	}
	if !strings.Contains(seed, "inspect the workspace") {
		t.Fatalf("missing files placeholder:\n%s", seed)
	}
}
