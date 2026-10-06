// Package workspace draws a line between "where the agent's code lives"
// and "where the agent is doing a task": the file tools resolve every
// path through a Workspace instead of touching os.* directly, so a model
// that emits "../../etc/passwd" gets an error rather than a write.
//
// It is a guardrail, not a sandbox, and the difference matters. run_shell
// executes arbitrary commands with only its working directory set to the
// root — nothing stops `del C:\...` — so a Workspace bounds mistakes by
// the file tools, not what a determined or badly-steered agent can reach.
// Real containment needs the process boundary (a container, a VM, a
// restricted user), not this type. Do not treat a Workspace as one, and
// do not harden it in ways that imply it is one.
//
// Resolve is textual: filepath.Clean handles "..", but a symlink or
// Windows junction inside the root that points outside is followed like
// any other path. See workspace_test.go, which documents that gap
// deliberately rather than papering over it.
package workspace

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Workspace struct {
	root         string
	instructions string
}

func New(root string) (*Workspace, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root: %w", err)
	}
	w := &Workspace{root: abs}
	var dirs []string
	for dir := abs; ; dir = filepath.Dir(dir) {
		dirs = append(dirs, dir)
		if dir == filepath.Dir(dir) {
			break
		}
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		path := filepath.Join(dirs[i], "AGENTS.md")
		f, err := os.Open(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(f, 64*1024+1))
		f.Close()
		if err != nil {
			return nil, err
		}
		if len(data) > 64*1024 {
			return nil, fmt.Errorf("instructions exceed 64 KiB: %s", path)
		}
		w.instructions += "\n\nProject instructions (" + path + "):\n" + string(data)
	}
	return w, nil
}

func (w *Workspace) Root() string { return w.root }

// Instructions returns ancestor and root AGENTS.md files, outermost first.
func (w *Workspace) Instructions() string {
	if w == nil {
		return ""
	}
	return w.instructions
}

// Resolve turns a path relative to the workspace into an absolute path,
// refusing anything that would escape the workspace root.
func (w *Workspace) Resolve(rel string) (string, error) {
	full := filepath.Clean(filepath.Join(w.root, rel))
	if full != w.root && !strings.HasPrefix(full, w.root+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes workspace root", rel)
	}
	return full, nil
}
