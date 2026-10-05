// Package tools holds concrete agent.Tool implementations: filesystem
// access, shell execution, and delegation to subagents. Each tool is a
// small, self-contained type — no shared "dispatcher" indirection.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/keshon/tars/internal/agent"
	"github.com/keshon/tars/internal/workspace"
)

// readMaxBytes caps one read_file result. Sized against real source
// files rather than a round number: the largest file in this repo is
// under 40KB, so 48KB reads any of them whole.
//
// The ceiling matters more than the headroom. Bytes are a poor proxy for
// context cost — 128KB of source is roughly 32k tokens, the same 128KB of
// base64 is 57k — and a local model often has 32-64k of context in total.
// A single tool result that can consume most of the window defeats
// everything else the loop does to protect it. Live: a read of a 205KB
// fixture returned the old 128KB cap, took the prompt from 3.4k to 57k
// tokens in one step, and the run hit its wall-clock limit at 900s.
//
// A truncated read still reports the file's true size, so the model can
// see what it did not get.
const readMaxBytes = 48 * 1024

type ReadFile struct{ WS *workspace.Workspace }

func (ReadFile) Name() string { return "read_file" }
func (ReadFile) Description() string {
	return "Read a text file. Returns a FILE header (path, size, lines, truncated, binary) and the " +
		"content, optionally from offset (a 1-based first line) for paging through large files. Set metadata_only=true to get only the header — use this for file size or " +
		"type checks without loading content into context."
}
func (ReadFile) Mode() agent.ToolMode { return agent.Concurrent }

// Idempotent: identical read_file calls return identical results until
// something mutates the workspace — the agent loop uses this to
// short-circuit exact-repeat calls instead of re-reading (see
// Registry.IdempotentOf).
func (ReadFile) Idempotent() bool { return true }

func (ReadFile) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string"},
			"metadata_only": {
				"type": "boolean",
				"description": "if true, return only the FILE header (size, truncated, binary) with no body"
			},
			"max_bytes": {
				"type": "integer",
				"description": "optional cap on content bytes; omit or 0 for the full content"
			},
			"offset": {
				"type": "integer",
				"description": "1-based first line to return; omit or 0 to start at the top. Use with the lines: count from a truncated read to page past it"
			}
		},
		"required": ["path"]
	}`)
}

func (t ReadFile) Run(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Path         string `json:"path"`
		MetadataOnly bool   `json:"metadata_only"`
		MaxBytes     *int   `json:"max_bytes"`
		Offset       int    `json:"offset"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if err := rejectUnknownFields(args, "path", "metadata_only", "max_bytes", "offset"); err != nil {
		return "", err
	}
	if strings.TrimSpace(in.Path) == "" {
		return "", fmt.Errorf("path is required and must be non-empty")
	}
	if in.Offset < 0 {
		return "", fmt.Errorf("offset must be >= 0")
	}
	full, err := t.WS.Resolve(in.Path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	rel, _ := filepath.Rel(t.WS.Root(), full)

	// max_bytes=0 (or omitted) means "no explicit cap", NOT metadata-only:
	// every model tested reads 0 as the universal "unlimited" convention.
	// The old 0-means-header-only semantics sent a weak model into a
	// repeat loop — 18 identical header-only reads in one live run,
	// because it kept asking for content with max_bytes=0 and kept
	// getting four lines of metadata. Header-only is spelled
	// metadata_only=true and nothing else.
	maxBody := readMaxBytes
	if in.MetadataOnly {
		maxBody = 0
	} else if in.MaxBytes != nil {
		if *in.MaxBytes < 0 {
			return "", fmt.Errorf("max_bytes must be >= 0")
		}
		if *in.MaxBytes > 0 {
			maxBody = *in.MaxBytes
		}
	}
	return formatReadFileResult(rel, data, maxBody, in.Offset), nil
}

// maxBodyBytes: 0 = header only; otherwise cap body at min(maxBodyBytes, readMaxBytes).
// offset is a 1-based first line; <=1 reads from the start, byte-identical
// to the old path. Line windows make large files pageable: the header
// always reports total lines (and the shown window when offset), and the
// truncation note points at the next offset instead of a dead end —
// without this, bytes past the cap are unreachable (max_bytes only
// shrinks the head, grep has no context lines), and the loop's
// duplicate-read refusal becomes a wall in front of nothing.
func formatReadFileResult(path string, data []byte, maxBodyBytes, offset int) string {
	totalSize := len(data)
	text, encoding, binary := decodeText(data)
	body := []byte(text)
	truncated := len(body) > readMaxBytes

	// Line inventory for paging. Empty file: zero lines, not one.
	var all []string
	if trimmed := strings.TrimSuffix(text, "\n"); trimmed != "" {
		all = strings.Split(trimmed, "\n")
	}
	totalLines := len(all)

	start := offset
	if start < 1 {
		start = 1
	}
	// Byte-exact tail from the requested line: scanning for newlines
	// preserves original bytes (and endings) without a rejoin.
	window := text
	winStart := 1
	pastEnd := false
	if totalLines > 0 && start > 1 {
		if start > totalLines {
			pastEnd = true
		} else {
			idx := 0
			for line := 1; line < start; idx++ {
				if text[idx] == '\n' {
					line++
				}
			}
			window = text[idx:]
			winStart = start
		}
	}

	var b strings.Builder
	b.WriteString("FILE\n")
	fmt.Fprintf(&b, "path: %s\n", path)
	fmt.Fprintf(&b, "size: %d\n", totalSize)
	fmt.Fprintf(&b, "lines: %d\n", totalLines)
	if encoding != "" {
		fmt.Fprintf(&b, "encoding: %s (decoded for display; patches are refused — convert to UTF-8 first)\n", encoding)
	}
	if truncated {
		b.WriteString("truncated: true\n")
	} else {
		b.WriteString("truncated: false\n")
	}
	if binary {
		b.WriteString("binary: true\n")
	}
	if maxBodyBytes == 0 {
		return b.String()
	}
	limit := maxBodyBytes
	if limit > readMaxBytes {
		limit = readMaxBytes
	}
	b.WriteString("----\n")
	if pastEnd {
		fmt.Fprintf(&b, "(offset %d past end of file: %d lines)", offset, totalLines)
		return b.String()
	}
	wbody := []byte(window)
	if len(wbody) > limit {
		shown := wbody[:limit]
		endLine := winStart + strings.Count(string(shown), "\n")
		if len(shown) > 0 && shown[len(shown)-1] == '\n' {
			endLine--
		}
		if winStart > 1 {
			fmt.Fprintf(&b, "window: lines %d-%d of %d\n", winStart, endLine, totalLines)
		}
		b.Write(shown)
		fmt.Fprintf(&b, "\n...(content truncated at %d of %d bytes, lines %d-%d of %d — re-read with offset %d to continue)",
			len(shown), len(wbody), winStart, endLine, totalLines, endLine+1)
		return b.String()
	}
	if winStart > 1 {
		endLine := winStart + strings.Count(window, "\n")
		if strings.HasSuffix(window, "\n") {
			endLine--
		}
		if endLine > totalLines {
			endLine = totalLines
		}
		fmt.Fprintf(&b, "window: lines %d-%d of %d\n", winStart, endLine, totalLines)
	}
	b.WriteString(window)
	return b.String()
}

func looksBinaryBytes(data []byte) bool {
	limit := len(data)
	if limit > grepSniffBytes {
		limit = grepSniffBytes
	}
	for _, b := range data[:limit] {
		if b == 0 {
			return true
		}
	}
	return false
}

// decodeText converts data for display: UTF-16LE/BE with BOM decodes to
// UTF-8, anything else passes through. Returns text, encoding label (""
// for plain UTF-8/bytes), and whether the raw bytes look binary. CRLF is
// normalized to LF so displayed text matches what patches will match.
func decodeText(data []byte) (text string, encoding string, binary bool) {
	if len(data) >= 2 && data[0] == 0xFF && data[1] == 0xFE {
		return normalizeNewlines(decodeUTF16(data[2:], true)), "utf-16le", false
	}
	if len(data) >= 2 && data[0] == 0xFE && data[1] == 0xFF {
		return normalizeNewlines(decodeUTF16(data[2:], false)), "utf-16be", false
	}
	return string(data), "", looksBinaryBytes(data)
}

func normalizeNewlines(s string) string {
	return strings.ReplaceAll(s, "\r\n", "\n")
}

// decodeUTF16 decodes units with the given byte order, dropping a
// trailing odd byte. No surrogate handling beyond the BMP — enough for a
// model to read the file; editing stays refused (see patch guards).
func decodeUTF16(data []byte, littleEndian bool) string {
	var out strings.Builder
	for i := 0; i+1 < len(data); i += 2 {
		var r rune
		if littleEndian {
			r = rune(data[i]) | rune(data[i+1])<<8
		} else {
			r = rune(data[i])<<8 | rune(data[i+1])
		}
		out.WriteRune(r)
	}
	return out.String()
}

// isUTF16 reports a BOM-prefixed UTF-16 file, which text patches refuse:
// byte-matching UTF-8 old_content against UTF-16 bytes never hits, and a
// silent re-encode would corrupt the file for its real consumer.
//
// Without this guard the model sees "old_content not found" forever and
// has no way to learn why: a live run circled for seven steps re-reading
// the same file, because every re-read came back as mojibake it could not
// match against the plain text it had just been shown.
func isUTF16(data []byte) bool {
	return len(data) >= 2 && ((data[0] == 0xFF && data[1] == 0xFE) ||
		(data[0] == 0xFE && data[1] == 0xFF))
}

// refuseUTF16 is the patch-side guard: name the encoding and the fix
// instead of letting the caller fail a match that can never succeed.
func refuseUTF16(tool, path string, data []byte) error {
	if !isUTF16(data) {
		return nil
	}
	enc := "utf-16le"
	if data[0] == 0xFE {
		enc = "utf-16be"
	}
	return fmt.Errorf("%s: %s is %s (BOM-prefixed, not UTF-8) — old_content can never match its bytes. "+
		"Convert it first (e.g. run_shell: python -c \"open('%s','rb').read().decode('utf-16').encode('utf-8')\" "+
		"written to the same path), then patch it", tool, path, enc, path)
}

type WriteFile struct{ WS *workspace.Workspace }

func (WriteFile) Name() string         { return "write_file" }
func (WriteFile) Mode() agent.ToolMode { return agent.Exclusive }
func (WriteFile) Description() string {
	return "Write text content to a file, creating parent directories as needed. " +
		"For content over a few KB, write in parts — write_file the head, then patch_file " +
		"to append — a single giant call risks truncation."
}
func (WriteFile) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string"},
			"content": {"type": "string"}
		},
		"required": ["path", "content"]
	}`)
}

func (t WriteFile) Run(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if err := rejectUnknownFields(args, "path", "content"); err != nil {
		return "", err
	}
	full, err := t.WS.Resolve(in.Path)
	if err != nil {
		return "", err
	}
	if err := guardWriteSize("write_file", in.Content); err != nil {
		return "", err
	}
	if msg := rewriteInsteadOfPatch(full, in.Path, in.Content); msg != "" {
		return "", fmt.Errorf("%s", msg)
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", err
	}
	res, err := withPathLock(in.Path, func() (string, error) {
		if err := os.WriteFile(full, []byte(in.Content), 0o644); err != nil {
			return "", err
		}
		return fmt.Sprintf("wrote %s (%d bytes)", in.Path, len(in.Content)), nil
	})
	if err != nil {
		return "", err
	}
	if note := gofmtNote(full); note != "" {
		res += "\n" + note
	}
	return res, nil
}

type ListFiles struct{ WS *workspace.Workspace }

func (ListFiles) Name() string         { return "list_files" }
func (ListFiles) Mode() agent.ToolMode { return agent.Concurrent }
func (ListFiles) Idempotent() bool     { return true }
func (ListFiles) Description() string {
	return "List files and directories under a path, non-recursively."
}
func (ListFiles) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {"path": {"type": "string"}},
		"required": ["path"]
	}`)
}

func (t ListFiles) Run(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	full, err := t.WS.Resolve(in.Path)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return "", err
	}
	out := ""
	for _, e := range entries {
		if e.IsDir() {
			out += e.Name() + "/\n"
		} else {
			out += e.Name() + "\n"
		}
	}
	return out, nil
}

// rewriteKeptRatio is how much of an existing file may survive a
// write_file before it is treated as an edit wearing a rewrite's clothes.
const rewriteKeptRatio = 0.7

// rewriteInsteadOfPatch refuses a write_file that replaces an existing
// file with something almost identical to it, and returns why.
//
// Rewriting a whole file to change one line is how surrounding code gets
// silently dropped: everything the model does not happen to reproduce is
// deleted, and nothing reports it. The system prompt has always asked for
// patch_file here, and asking is not enough — a live probe run rewrote a
// file to change one constant despite the rule.
//
// The test is how much of the old file survives. A genuine rewrite shares
// little with what it replaces and passes; changing a constant in a file
// that is otherwise reproduced verbatim does not. New files, tiny files
// and wholesale replacements are all unaffected.
func rewriteInsteadOfPatch(full, rel, content string) string {
	old, err := os.ReadFile(full)
	if err != nil || len(old) == 0 {
		return "" // new file, or unreadable: nothing to protect
	}
	oldLines := strings.Split(string(old), "\n")
	if len(oldLines) < 5 {
		return "" // too small for a patch to be the clearly better tool
	}
	newLines := map[string]int{}
	for _, l := range strings.Split(content, "\n") {
		newLines[strings.TrimRight(l, "\r")]++
	}
	kept := 0
	for _, l := range oldLines {
		l = strings.TrimRight(l, "\r")
		if newLines[l] > 0 {
			newLines[l]--
			kept++
		}
	}
	if float64(kept)/float64(len(oldLines)) < rewriteKeptRatio {
		return "" // a real rewrite, not an edit in disguise
	}
	return fmt.Sprintf("refusing to rewrite %s: %d of its %d lines are unchanged, so this is an "+
		"edit, not a rewrite. Rewriting a whole file silently drops everything you did not "+
		"reproduce. Use patch_file with the exact text you want changed, or patch_lines for a "+
		"line range", rel, kept, len(oldLines))
}
