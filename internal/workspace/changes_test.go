package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestObserverMeasuresActualEffects(t *testing.T) {
	root := t.TempDir()
	ws, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0600)
	observe, err := ws.ChangeObserver(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0600)
	if paths, err := observe(); err != nil || len(paths) != 0 {
		t.Fatalf("same bytes counted: %v %v", paths, err)
	}
	os.Remove(filepath.Join(root, "a.txt"))
	os.WriteFile(filepath.Join(root, "b.txt"), []byte("b"), 0600)
	if paths, err := observe(); err != nil || !reflect.DeepEqual(paths, []string{"a.txt", "b.txt"}) {
		t.Fatalf("effects=%v %v", paths, err)
	}
}
func TestFileSetHonorsGitIgnoreAndTrackedExceptions(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init").CombinedOutput(); err != nil {
		t.Fatalf("git: %v %s", err, out)
	}
	os.MkdirAll(filepath.Join(root, "build"), 0755)
	os.WriteFile(filepath.Join(root, ".gitignore"), []byte("build/\n*.log\n"), 0600)
	os.WriteFile(filepath.Join(root, "build", "source.go"), []byte("package fixture"), 0600)
	exec.Command("git", "-C", root, "add", "-f", "build/source.go").Run()
	os.WriteFile(filepath.Join(root, "build", "artifact"), []byte("generated"), 0600)
	os.WriteFile(filepath.Join(root, "secret.log"), []byte("ignored"), 0600)
	ws, _ := New(root)
	files, err := ws.FileSet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !files[filepath.Join("build", "source.go")] || files[filepath.Join("build", "artifact")] || files["secret.log"] {
		t.Fatalf("files=%v", files)
	}
}
func TestInstructionsLoadAncestorsBeforeRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "project")
	os.MkdirAll(root, 0755)
	os.WriteFile(filepath.Join(parent, "AGENTS.md"), []byte("outer rule"), 0600)
	os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("inner rule"), 0600)
	ws, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	text := ws.Instructions()
	if strings.Index(text, "outer rule") < 0 || strings.Index(text, "inner rule") < strings.Index(text, "outer rule") {
		t.Fatal(text)
	}
}
