package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/keshon/tars/internal/agent"
	"github.com/keshon/tars/internal/workspace"
)

// PatchLines replaces lines [start_line, end_line] (1-indexed, inclusive)
// with new_content. Complements PatchFile for edits where the target text
// isn't unique enough to match by content (e.g. "fix line 42").
type PatchLines struct{ WS *workspace.Workspace }

func (PatchLines) Name() string         { return "patch_lines" }
func (PatchLines) Mode() agent.ToolMode { return agent.Exclusive }
func (PatchLines) Description() string {
	return "Replace a range of lines (1-indexed, inclusive) in a file with new content. " +
		"Use this when editing by line number is clearer than matching exact text with patch_file."
}
func (PatchLines) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string"},
			"start_line": {"type": "integer"},
			"end_line": {"type": "integer"},
			"new_content": {"type": "string"}
		},
		"required": ["path", "start_line", "end_line", "new_content"]
	}`)
}

func (t PatchLines) Run(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Path       string `json:"path"`
		StartLine  int    `json:"start_line"`
		EndLine    int    `json:"end_line"`
		NewContent string `json:"new_content"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if err := rejectUnknownFields(args, "path", "start_line", "end_line", "new_content"); err != nil {
		return "", err
	}
	if err := guardWriteSize("patch_lines", in.NewContent); err != nil {
		return "", err
	}
	if in.StartLine < 1 || in.EndLine < in.StartLine {
		return "", fmt.Errorf("invalid range: start_line=%d end_line=%d", in.StartLine, in.EndLine)
	}

	full, err := t.WS.Resolve(in.Path)
	if err != nil {
		return "", err
	}
	return withPathLock(in.Path, func() (string, error) {
		data, err := os.ReadFile(full)
		if err != nil {
			return "", err
		}
		if err := refuseUTF16("patch_lines", in.Path, data); err != nil {
			return "", err
		}

		content, ending := normalizeContent(data)
		lines := strings.Split(content, "\n")
		if in.StartLine > len(lines) {
			return "", fmt.Errorf("start_line %d is beyond end of file (%d lines)", in.StartLine, len(lines))
		}
		end := in.EndLine
		if end > len(lines) {
			end = len(lines)
		}

		replacement, _ := normalizeContent([]byte(in.NewContent))
		// CRLF files: split on \n after normalization, compare fairly.
		newSeg := strings.Split(replacement, "\n")
		oldSeg := lines[in.StartLine-1 : end]
		if strings.Join(oldSeg, "\n") == strings.Join(newSeg, "\n") {
			return "", fmt.Errorf("patch_lines: replacement is identical to lines %d-%d — nothing would change", in.StartLine, end)
		}

		result := append([]string{}, lines[:in.StartLine-1]...)
		result = append(result, newSeg...)
		result = append(result, lines[end:]...)

		if err := os.WriteFile(full, []byte(denormalize(strings.Join(result, "\n"), ending)), 0o644); err != nil {
			return "", err
		}
		out := "ok\n" + unifiedDiff(in.Path, lines, in.StartLine-1, end, newSeg)
		if note := gofmtNote(full); note != "" {
			out += "\n" + note
		}
		return out, nil
	})
}
