// Package snapshot captures the workspace git diff before a run so work
// can be reviewed or reverted afterwards. It shells out to git (no new
// dependencies) and degrades to "not a repo" rather than failing: a
// non-git workspace simply gets no snapshot.
package snapshot

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Snapshot is a captured diff on disk.
type Snapshot struct {
	Path string // patch file, "" when not a repo
	Root string
}

// Available reports whether root is inside a git work tree.
func Available(root string) bool {
	cmd := exec.Command("git", "-C", root, "rev-parse", "--is-inside-work-tree")
	if err := cmd.Run(); err != nil {
		return false
	}
	return true
}

// Track captures the current diff (tracked + untracked via --no-index? no:
// tracked only, untracked listed separately) into dir and returns it.
// Best-effort: any failure returns an empty Snapshot, never an error.
func Track(root, dir string) Snapshot {
	if !Available(root) {
		return Snapshot{Root: root}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Snapshot{Root: root}
	}
	path := filepath.Join(dir, fmt.Sprintf("snapshot-%d.diff", time.Now().UnixNano()))
	diff, _ := Diff(root)
	if err := os.WriteFile(path, []byte(diff), 0o644); err != nil {
		return Snapshot{Root: root}
	}
	return Snapshot{Path: path, Root: root}
}

// Diff returns `git diff HEAD` plus untracked file listing for root.
func Diff(root string) (string, error) {
	diff, err := exec.Command("git", "-C", root, "diff", "HEAD", "--", ".").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git diff: %w", err)
	}
	untracked, _ := exec.Command("git", "-C", root, "ls-files", "--others", "--exclude-standard").CombinedOutput()
	out := string(diff)
	if len(untracked) > 0 {
		out += "\n# untracked:\n" + string(untracked)
	}
	return out, nil
}

// Revert restores tracked files to HEAD (git checkout) — untracked files
// are left alone, so model-created files are never silently deleted.
// Returns an error only when git itself fails.
func Revert(root string) error {
	if !Available(root) {
		return fmt.Errorf("not a git repo: %s", root)
	}
	if out, err := exec.Command("git", "-C", root, "checkout", "--", ".").CombinedOutput(); err != nil {
		return fmt.Errorf("git checkout: %w: %s", err, string(out))
	}
	return nil
}
