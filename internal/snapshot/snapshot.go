// Package snapshot captures workspace files and the Git index before a run so work
// can be reviewed or reverted afterwards. It shells out to git (no new
// dependencies) and degrades to "not a repo" rather than failing: a
// non-git workspace simply gets no snapshot.
package snapshot

import (
	"archive/zip"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Snapshot is a pre-run checkpoint archive.
type Snapshot struct {
	Path string // archive, "" when capture failed or the workspace is not a repo
	Root string
}

// Available reports whether root is inside a git work tree.
func Available(root string) bool {
	cmd := exec.Command("git", "-C", root, "rev-parse", "--is-inside-work-tree")
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// Track saves a complete checkpoint of tracked and non-ignored files and the
// index. A missing Path means no checkpoint was made; callers must report it.
func Track(root, dir string) Snapshot {
	snap := Snapshot{Root: root}
	if !Available(root) {
		return snap
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return snap
	}
	path, err := filepath.Abs(filepath.Join(dir, fmt.Sprintf("snapshot-%d.zip", time.Now().UnixNano())))
	if err != nil {
		return snap
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return snap
	}
	z := zip.NewWriter(f)
	err = capture(root, z)
	if e := z.Close(); err == nil {
		err = e
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		os.Remove(path)
		return snap
	}
	pointer := filepath.Join(root, ".tars", "latest-snapshot")
	if err = os.MkdirAll(filepath.Dir(pointer), 0755); err == nil {
		err = os.WriteFile(pointer, []byte(path), 0600)
	}
	if err != nil {
		return snap
	}
	snap.Path = path
	return snap
}

func paths(root string) ([]string, error) {
	out, err := exec.Command("git", "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard").Output()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var result []string
	for _, name := range strings.Split(string(out), "\x00") {
		if name == "" || name == ".tars" || strings.HasPrefix(name, ".tars/") || seen[name] {
			continue
		}
		if _, err := safePath(root, name); err != nil {
			return nil, err
		}
		seen[name] = true
		result = append(result, name)
	}
	return result, nil
}

func safePath(root, name string) (string, error) {
	full := filepath.Join(root, filepath.FromSlash(name))
	rel, err := filepath.Rel(root, full)
	if name == "" || name == "." || name == ".git" || name == ".tars" || strings.HasPrefix(filepath.ToSlash(name), ".git/") || strings.HasPrefix(filepath.ToSlash(name), ".tars/") || err != nil || filepath.IsAbs(name) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe checkpoint path: %q", name)
	}
	return full, nil
}

func indexPath(root string) (string, error) {
	out, err := exec.Command("git", "-C", root, "rev-parse", "--path-format=absolute", "--git-path", "index").Output()
	return strings.TrimSpace(string(out)), err
}

func capture(root string, z *zip.Writer) error {
	names, err := paths(root)
	if err != nil {
		return err
	}
	for _, name := range names {
		full, _ := safePath(root, name)
		info, err := os.Lstat(full)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.IsDir() {
			return fmt.Errorf("submodule checkpoint unsupported: %s", name)
		}
		h, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		h.Name = "files/" + name
		h.Method = zip.Deflate
		w, err := z.CreateHeader(h)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(full)
			if err != nil {
				return err
			}
			if _, err = io.WriteString(w, target); err != nil {
				return err
			}
		} else {
			f, err := os.Open(full)
			if err != nil {
				return err
			}
			_, err = io.Copy(w, f)
			f.Close()
			if err != nil {
				return err
			}
		}
	}
	index, err := indexPath(root)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(index)
	if err != nil {
		return err
	}
	head, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		return err
	}
	hw, err := z.Create("head")
	if err != nil {
		return err
	}
	if _, err = hw.Write(head); err != nil {
		return err
	}
	w, err := z.Create("index")
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
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

// Revert restores the latest pre-run checkpoint, including the index and
// pre-existing untracked files. Newly created non-ignored files are removed.
func Revert(root string) error {
	path, err := os.ReadFile(filepath.Join(root, ".tars", "latest-snapshot"))
	if err != nil {
		return fmt.Errorf("no pre-run checkpoint: %w", err)
	}
	return Restore(root, strings.TrimSpace(string(path)))
}

// Restore restores a particular checkpoint. Validate all archive entries and
// checksums before modifying the workspace.
func Restore(root, path string) error {
	z, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer z.Close()
	stage, err := os.MkdirTemp("", "tars-restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	type entry struct {
		name   string
		mode   os.FileMode
		staged string
	}
	var entries []entry
	var index, head string
	present := map[string]bool{}
	symlinks := map[string]bool{}
	for i, f := range z.File {
		if f.Name != "index" && f.Name != "head" && !strings.HasPrefix(f.Name, "files/") {
			return fmt.Errorf("invalid checkpoint entry: %s", f.Name)
		}
		name := strings.TrimPrefix(f.Name, "files/")
		if f.Name != "index" && f.Name != "head" {
			if _, err := safePath(root, name); err != nil {
				return err
			}
			if present[name] {
				return fmt.Errorf("duplicate checkpoint entry: %s", name)
			}
			present[name] = true
			if f.Mode()&os.ModeSymlink != 0 {
				symlinks[name] = true
			}
		}
		source, err := f.Open()
		if err != nil {
			return err
		}
		staged := filepath.Join(stage, fmt.Sprint(i))
		dest, err := os.Create(staged)
		if err != nil {
			source.Close()
			return err
		}
		_, copyErr := io.Copy(dest, source)
		source.Close()
		closeErr := dest.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		switch f.Name {
		case "index":
			index = staged
		case "head":
			head = staged
		default:
			entries = append(entries, entry{name, f.Mode(), staged})
		}
	}
	if index == "" || head == "" {
		return fmt.Errorf("checkpoint missing index or HEAD")
	}
	savedHead, err := os.ReadFile(head)
	if err != nil {
		return err
	}
	currentHead, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		return err
	}
	if string(currentHead) != string(savedHead) {
		return fmt.Errorf("HEAD changed since checkpoint; restore requires the original commit")
	}
	names, err := paths(root)
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := validateParents(root, name); err != nil {
			return err
		}
	}
	for _, e := range entries {
		if err := validateParents(root, e.name); err != nil {
			return err
		}
		for parent := filepath.ToSlash(filepath.Dir(e.name)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
			if symlinks[parent] {
				return fmt.Errorf("checkpoint entry under symlink: %s", e.name)
			}
		}
	}
	for _, name := range names {
		if !present[name] {
			full, _ := safePath(root, name)
			if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	for _, e := range entries {
		full, _ := safePath(root, e.name)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			return err
		}
		if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
			return err
		}
		if e.mode&os.ModeSymlink != 0 {
			data, err := os.ReadFile(e.staged)
			if err != nil {
				return err
			}
			if err := os.Symlink(string(data), full); err != nil {
				return err
			}
		} else if err := copyFile(e.staged, full, e.mode.Perm()); err != nil {
			return err
		}
	}
	ip, err := indexPath(root)
	if err != nil {
		return err
	}
	return copyFile(index, ip, 0600)
}

func copyFile(source, dest string, mode os.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	closeErr := out.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func validateParents(root, name string) error {
	full, err := safePath(root, name)
	if err != nil {
		return err
	}
	for parent := filepath.Dir(full); parent != root && parent != filepath.Dir(parent); parent = filepath.Dir(parent) {
		if info, err := os.Lstat(parent); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("checkpoint parent is a symlink: %s", parent)
		}
	}
	return nil
}

// Preview lists the file and index changes Restore would make, without writing.
// An empty path selects the latest checkpoint.
func Preview(root, path string) ([]string, error) {
	if path == "" {
		data, err := os.ReadFile(filepath.Join(root, ".tars", "latest-snapshot"))
		if err != nil {
			return nil, err
		}
		path = strings.TrimSpace(string(data))
	}
	z, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer z.Close()
	present := map[string]bool{}
	var changes []string
	for _, entry := range z.File {
		if entry.Name == "head" {
			continue
		}
		var full, label string
		if entry.Name == "index" {
			full, err = indexPath(root)
			label = "restore Git index"
		} else {
			if !strings.HasPrefix(entry.Name, "files/") {
				return nil, fmt.Errorf("invalid checkpoint entry: %s", entry.Name)
			}
			name := strings.TrimPrefix(entry.Name, "files/")
			present[name] = true
			full, err = safePath(root, name)
			label = "restore " + name
		}
		if err != nil {
			return nil, err
		}
		info, err := os.Lstat(full)
		if os.IsNotExist(err) {
			changes = append(changes, label)
			continue
		}
		if err != nil {
			return nil, err
		}
		var sum uint32
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(full)
			if err != nil {
				return nil, err
			}
			sum = crc32.ChecksumIEEE([]byte(target))
		} else if info.IsDir() {
			changes = append(changes, label)
			continue
		} else {
			f, err := os.Open(full)
			if err != nil {
				return nil, err
			}
			h := crc32.NewIEEE()
			_, err = io.Copy(h, f)
			f.Close()
			if err != nil {
				return nil, err
			}
			sum = h.Sum32()
		}
		if sum != entry.CRC32 || (entry.Name != "index" && (info.Mode().Type() != entry.Mode().Type() || info.Mode().Perm() != entry.Mode().Perm())) {
			changes = append(changes, label)
		}
	}
	names, err := paths(root)
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		if !present[name] {
			if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(name))); os.IsNotExist(err) {
				continue
			} else if err != nil {
				return nil, err
			}
			changes = append(changes, "remove "+name)
		}
	}
	sort.Strings(changes)
	return changes, nil
}
