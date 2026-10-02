package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestProbeCommand_Missing(t *testing.T) {
	got := ProbeCommand(context.Background(), "definitely-not-a-real-binary-tars-probe")
	if got.Status != "missing" || got.Hint == "" {
		t.Fatalf("got %+v, want missing with hint", got)
	}
}

func TestProbeCommand_Ok(t *testing.T) {
	// Whatever proves a binary runs must itself run here: go is present
	// wherever `go test` runs. `go version` answers with no side effects.
	got := ProbeCommand(context.Background(), "go", "version")
	if got.Status != "ok" {
		t.Fatalf("got %+v, want ok", got)
	}
}

func TestProbeCommand_UnknownFlagStillOk(t *testing.T) {
	// A tool that dislikes the probe flag still proves it exists by
	// starting: `go --no-such-flag` exits 2, which is an existence proof.
	got := ProbeCommand(context.Background(), "go", "--no-such-flag-tars-probe")
	if got.Status != "ok" {
		t.Fatalf("got %+v, want ok", got)
	}
}

func TestProbeCommand_BrokenShim(t *testing.T) {
	// A name LookPath resolves but the OS cannot start.
	// Unix: an executable script with a dead shebang — the stale-shim
	// shape itself (wrappers pointing at deleted interpreters). A bare
	// non-executable file does NOT work as a fixture: Unix LookPath
	// checks the exec bit and reports "missing", which is the correct
	// verdict for it, not the broken path under test.
	dir := t.TempDir()
	name := "deadshim"
	content := "#!/nonexistent-interpreter-tars-probe\nexit 0\n"
	mode := os.FileMode(0o700)
	if runtime.GOOS == "windows" {
		// cmd.exe interprets any .cmd text (even garbage exits 0), so a
		// batch fixture cannot be broken. A garbage .exe makes the
		// loader itself refuse: "not a valid Win32 application".
		name = "deadshim.exe"
		content = "\x00\x01\x02 not executable content"
		mode = 0o600
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	got := ProbeCommand(context.Background(), strings.TrimSuffix(strings.TrimSuffix(name, ".cmd"), ".exe"), "--version")
	if got.Status != "broken" || got.Hint == "" {
		t.Fatalf("got %+v, want broken with hint", got)
	}
}

func TestProbeCommand_Timeout(t *testing.T) {
	// An already-expired context exercises the timeout mapping without
	// needing a real sleeper (which would be `sleep` vs `ping -n` again).
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	got := ProbeCommand(ctx, "go", "version")
	if got.Status != "timeout" || !strings.Contains(got.Hint, "hang") {
		t.Fatalf("got %+v, want timeout", got)
	}
}
