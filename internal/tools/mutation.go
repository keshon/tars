package tools

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

// pathLocks serializes mutating tools per workspace path: two Exclusive
// calls in one step already run in order, but Concurrent batches and
// background workers can interleave a read with a write of the same file.
var pathLocks = struct {
	mu sync.Mutex
	m  map[string]*sync.Mutex
}{m: map[string]*sync.Mutex{}}

// withPathLock runs fn holding the mutex for path.
func withPathLock(path string, fn func() (string, error)) (string, error) {
	key := strings.ReplaceAll(strings.ToLower(path), "\\", "/")
	pathLocks.mu.Lock()
	l, ok := pathLocks.m[key]
	if !ok {
		l = &sync.Mutex{}
		pathLocks.m[key] = l
	}
	pathLocks.mu.Unlock()
	l.Lock()
	defer l.Unlock()
	return fn()
}

// normalizeContent strips a BOM and converts CRLF to LF for matching.
// Returns the normalized text and the file's original line ending.
func normalizeContent(data []byte) (text, ending string) {
	s := string(data)
	s = strings.TrimPrefix(s, "\xef\xbb\xbf")
	ending = "\n"
	if strings.Contains(s, "\r\n") {
		ending = "\r\n"
	}
	return strings.ReplaceAll(s, "\r\n", "\n"), ending
}

// denormalize converts LF back to the file's original line ending.
func denormalize(s, ending string) string {
	if ending == "\r\n" {
		return strings.ReplaceAll(s, "\n", "\r\n")
	}
	return s
}

// guardEdit rejects empty or no-op edits before touching disk.
func guardEdit(tool, oldContent, newContent string) error {
	if strings.TrimSpace(oldContent) == "" {
		return fmt.Errorf("%s: old_content is empty — match the exact text to replace", tool)
	}
	if oldContent == newContent {
		return fmt.Errorf("%s: old_content and new_content are identical — nothing would change", tool)
	}
	return guardWriteSize(tool, newContent)
}

// writeMaxBytes caps one file-writing call's new content. Refused,
// never truncated: a cut write corrupts silently, and legitimate
// artifacts rarely approach a megabyte (reads cap at 48K; the loop
// re-reads what it writes).
const writeMaxBytes = 1024 * 1024

// guardWriteSize refuses oversized writes before they touch disk.
func guardWriteSize(tool, content string) error {
	if len(content) > writeMaxBytes {
		return fmt.Errorf("%s: content %d bytes exceeds write cap %d — split the write", tool, len(content), writeMaxBytes)
	}
	return nil
}

// unifiedDiff renders oldLines[start:start+len(newSeg)] → newSeg with 3
// lines of context, in a compact unified-hunk shape for the tool result.
func unifiedDiff(path string, oldLines []string, start, end int, newSeg []string) string {
	cs := start - 3
	if cs < 0 {
		cs = 0
	}
	ce := end + 3
	if ce > len(oldLines) {
		ce = len(oldLines)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "--- %s\n+++ %s\n", path, path)
	fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", cs+1, ce-cs, cs+1, cs+len(newSeg)+(ce-end))
	for _, l := range oldLines[cs:start] {
		b.WriteString(" " + l + "\n")
	}
	for _, l := range oldLines[start:end] {
		b.WriteString("-" + l + "\n")
	}
	for _, l := range newSeg {
		b.WriteString("+" + l + "\n")
	}
	for _, l := range oldLines[end:ce] {
		b.WriteString(" " + l + "\n")
	}
	return b.String()
}

// gofmtNote runs gofmt -l on a .go file after an edit. Best-effort: empty
// when gofmt is missing, the file isn't Go, or it's already formatted.
func gofmtNote(full string) string {
	if !strings.HasSuffix(strings.ToLower(full), ".go") {
		return ""
	}
	gofmt, err := exec.LookPath("gofmt")
	if err != nil {
		return ""
	}
	out, err := exec.Command(gofmt, "-l", full).Output()
	if err != nil {
		return ""
	}
	if len(strings.TrimSpace(string(out))) > 0 {
		return "note: gofmt would reformat this file — run gofmt -w " + full
	}
	return ""
}
