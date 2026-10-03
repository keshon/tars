package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/keshon/tars/internal/workspace"
)

// previewMaxLines caps a preview: approval prompts share context with
// the work, so the evidence must stay small.
const previewMaxLines = 40

// PreviewArgs renders what a mutating call is about to do, for approval
// prompts. patch_file yields the unified diff the call would produce;
// write_file shows new-file size or a capped diff against the existing
// file; move_file shows the rename. Anything else (or unparseable args,
// missing files, UTF-16) returns "" — no preview beats a wrong one, and
// the gate still asks with the tool name and resource.
func PreviewArgs(ws *workspace.Workspace, tool string, args json.RawMessage) string {
	switch tool {
	case "patch_file":
		return previewPatch(ws, args)
	case "write_file":
		return previewWrite(ws, args)
	case "move_file":
		return previewMove(args)
	default:
		return ""
	}
}

func previewPatch(ws *workspace.Workspace, args json.RawMessage) string {
	var in struct {
		Path       string `json:"path"`
		OldContent string `json:"old_content"`
		NewContent string `json:"new_content"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return ""
	}
	if err := guardEdit("patch_file", in.OldContent, in.NewContent); err != nil {
		return ""
	}
	full, err := ws.Resolve(in.Path)
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return ""
	}
	if isUTF16(data) {
		return ""
	}
	content, _ := normalizeContent(data)
	oldNorm, _ := normalizeContent([]byte(in.OldContent))
	count := strings.Count(content, oldNorm)
	if count == 0 {
		return "(old_content not found — this patch would fail)"
	}
	if count > 1 {
		return "(old_content matches multiple places — this patch would fail)"
	}
	newContent := strings.Replace(content, oldNorm, in.NewContent, 1)
	oldLines := strings.Split(content, "\n")
	newLines := strings.Split(newContent, "\n")
	return capLines(unifiedDiff(in.Path, oldLines, 0, len(oldLines), newLines), previewMaxLines)
}

func previewWrite(ws *workspace.Workspace, args json.RawMessage) string {
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return ""
	}
	full, err := ws.Resolve(in.Path)
	if err != nil {
		return ""
	}
	old, err := os.ReadFile(full)
	if err != nil {
		n := len(strings.Split(in.Content, "\n"))
		return fmt.Sprintf("(new file, %d lines)", n)
	}
	if isUTF16(old) {
		return ""
	}
	oldText, _ := normalizeContent(old)
	newText, _ := normalizeContent([]byte(in.Content))
	if oldText == newText {
		return "(identical to the file on disk — this write changes nothing)"
	}
	return capLines(unifiedDiff(in.Path,
		strings.Split(oldText, "\n"), 0, len(strings.Split(oldText, "\n")),
		strings.Split(newText, "\n")), previewMaxLines)
}

func previewMove(args json.RawMessage) string {
	var in struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return ""
	}
	if in.From == "" || in.To == "" {
		return ""
	}
	return fmt.Sprintf("rename %s -> %s", in.From, in.To)
}

func capLines(s string, max int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= max {
		return s
	}
	return strings.Join(lines[:max], "\n") +
		fmt.Sprintf("\n... (%d more lines)", len(lines)-max)
}
