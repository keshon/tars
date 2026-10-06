package workspace

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

func IgnoredDirectory(name string) bool {
	switch name {
	case ".git", ".tars", "node_modules", ".venv", "venv", "__pycache__", "target", "dist", "build":
		return true
	}
	return false
}

type fileStamp struct {
	Size     int64
	Modified int64
	Mode     fs.FileMode
	Digest   [32]byte
}

// ChangeObserver measures effects regardless of which tool made them. Digests
// are reused for files whose size and modification timestamp are unchanged.
func (w *Workspace) ChangeObserver(ctx context.Context) (func() ([]string, error), error) {
	previous, err := w.inventory(ctx, nil)
	if err != nil {
		return nil, err
	}
	return func() ([]string, error) {
		next, err := w.inventory(ctx, previous)
		if err != nil {
			return nil, err
		}
		var changed []string
		for p, s := range next {
			if old, ok := previous[p]; !ok || old.Digest != s.Digest || old.Mode != s.Mode {
				changed = append(changed, p)
			}
		}
		for p := range previous {
			if _, ok := next[p]; !ok {
				changed = append(changed, p)
			}
		}
		previous = next
		sort.Strings(changed)
		return changed, nil
	}, nil
}

func (w *Workspace) inventory(ctx context.Context, previous map[string]fileStamp) (map[string]fileStamp, error) {
	files, err := w.FileSet(ctx)
	if err != nil {
		return nil, err
	}
	dirs := FileDirectories(files)
	result := map[string]fileStamp{}
	err = filepath.WalkDir(w.root, func(path string, d fs.DirEntry, err error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err != nil {
			return err
		}
		if d.IsDir() {
			if dirs != nil {
				rel, _ := filepath.Rel(w.root, path)
				if !dirs[rel] {
					return filepath.SkipDir
				}
			}
			if path != w.root && (d.Name() == ".git" || d.Name() == ".tars" || (files == nil && IgnoredDirectory(d.Name()) && d.Name() != "dist" && d.Name() != "build" && d.Name() != "target")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() && d.Type()&os.ModeSymlink == 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(w.root, path)
		if err != nil {
			return err
		}
		if files != nil && !files[rel] {
			return nil
		}
		if len(result) >= 100000 {
			return fmt.Errorf("workspace inventory exceeds 100000 files")
		}
		rel = filepath.ToSlash(rel)
		stamp := fileStamp{Size: info.Size(), Modified: info.ModTime().UnixNano(), Mode: info.Mode()}
		if old, ok := previous[rel]; ok && old.Size == stamp.Size && old.Modified == stamp.Modified && old.Mode == stamp.Mode {
			result[rel] = old
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			stamp.Digest = sha256.Sum256([]byte(target))
			result[rel] = stamp
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		h := sha256.New()
		_, err = io.Copy(h, contextReader{ctx, f})
		f.Close()
		if err != nil {
			return err
		}
		copy(stamp.Digest[:], h.Sum(nil))
		result[rel] = stamp
		return nil
	})
	return result, err
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
