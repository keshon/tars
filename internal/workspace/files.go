package workspace

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// FileSet uses Git's ignore rules, including tracked files in ignored folders.
// Nil means a non-Git workspace; callers use conventional directory exclusions.
func (w *Workspace) FileSet(ctx context.Context) (map[string]bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", w.root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 128 {
			return nil, nil
		}
		if errors.Is(err, exec.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	files := map[string]bool{}
	for _, name := range strings.Split(string(out), "\x00") {
		if name == "" || name == ".tars" || strings.HasPrefix(name, ".tars/") {
			continue
		}
		files[filepath.Clean(filepath.FromSlash(name))] = true
	}
	return files, nil
}

// FileDirectories indexes ancestors so walkers can prune ignored subtrees.
func FileDirectories(files map[string]bool) map[string]bool {
	if files == nil {
		return nil
	}
	dirs := map[string]bool{".": true}
	for name := range files {
		for dir := filepath.Dir(name); dir != "."; dir = filepath.Dir(dir) {
			dirs[dir] = true
		}
	}
	return dirs
}
