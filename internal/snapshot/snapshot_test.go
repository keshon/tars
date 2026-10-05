package snapshot

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init")
	run("commit", "--allow-empty", "-m", "init")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	run("commit", "-m", "a")
	return dir
}

func TestAvailable(t *testing.T) {
	if !Available(initRepo(t)) {
		t.Error("expected repo")
	}
	if Available(t.TempDir()) {
		t.Error("empty dir must not be a repo")
	}
}

func TestTrackAndDiff(t *testing.T) {
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	snap := Track(dir, filepath.Join(dir, ".tars", "snapshots"))
	if snap.Path == "" {
		t.Fatal("expected snapshot path")
	}
	diff, err := Diff(dir)
	if err != nil || diff == "" {
		t.Fatalf("diff=%q err=%v", diff, err)
	}
}

func TestRevertRestoresTracked(t *testing.T) {
	dir := initRepo(t)
	p := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(p, []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Revert(dir); err != nil {
		t.Fatalf("revert: %v", err)
	}
	data, _ := os.ReadFile(p)
	if string(data) != "v1" {
		t.Fatalf("got %q, want v1", data)
	}
}

func TestTrackNonRepo(t *testing.T) {
	if snap := Track(t.TempDir(), t.TempDir()); snap.Path != "" {
		t.Fatalf("expected empty snapshot, got %s", snap.Path)
	}
}
