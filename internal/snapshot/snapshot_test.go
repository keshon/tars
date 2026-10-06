package snapshot

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
	Track(dir, filepath.Join(dir, ".tars", "snapshots"))
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

func TestCheckpointPreservesDirtyIndexAndUntracked(t *testing.T) {
	root := initRepo(t)
	a := filepath.Join(root, "a.txt")
	seed := filepath.Join(root, "seed.txt")
	os.WriteFile(a, []byte("staged"), 0600)
	if out, err := exec.Command("git", "-C", root, "add", "a.txt").CombinedOutput(); err != nil {
		t.Fatalf("stage: %v %s", err, out)
	}
	os.WriteFile(a, []byte("unstaged"), 0600)
	os.WriteFile(seed, []byte("seed"), 0600)
	snap := Track(root, filepath.Join(root, ".tars", "snapshots"))
	if snap.Path == "" {
		t.Fatal("capture failed")
	}
	os.WriteFile(a, []byte("agent"), 0600)
	os.Remove(seed)
	os.WriteFile(filepath.Join(root, "new.txt"), []byte("new"), 0600)
	exec.Command("git", "-C", root, "add", "-A").Run()
	if err := Revert(root); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(a); string(data) != "unstaged" {
		t.Fatalf("working copy=%q", data)
	}
	if data, _ := exec.Command("git", "-C", root, "show", ":a.txt").Output(); string(data) != "staged" {
		t.Fatalf("index=%q", data)
	}
	if data, _ := os.ReadFile(seed); string(data) != "seed" {
		t.Fatalf("seed=%q", data)
	}
	if _, err := os.Stat(filepath.Join(root, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("new file survives: %v", err)
	}
}

func TestRevertRequiresCheckpoint(t *testing.T) {
	root := initRepo(t)
	if Revert(root) == nil {
		t.Fatal("revert without checkpoint allowed")
	}
}

func TestCheckpointPreviewDoesNotModifyFiles(t *testing.T) {
	root := initRepo(t)
	snap := Track(root, filepath.Join(root, ".tars", "snapshots"))
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("agent"), 0600)
	os.WriteFile(filepath.Join(root, "new.txt"), []byte("new"), 0600)
	changes, err := Preview(root, snap.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(changes, []string{"remove new.txt", "restore a.txt"}) {
		t.Fatalf("preview=%v", changes)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(data) != "agent" {
		t.Fatal("preview changed workspace")
	}
}
