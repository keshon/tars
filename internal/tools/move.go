package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/keshon/tars/internal/agent"
	"github.com/keshon/tars/internal/workspace"
)

// MoveFile renames or moves a file in one atomic step. It exists
// specifically so a model never has to compose read_file + write_file +
// a delete to do a rename — that composition is exactly where a weak
// model can "succeed" by writing an empty placeholder instead of the
// real content.
type MoveFile struct{ WS *workspace.Workspace }

func (MoveFile) Name() string         { return "move_file" }
func (MoveFile) Mode() agent.ToolMode { return agent.Exclusive }
func (MoveFile) Description() string {
	return "Rename or move a file within the workspace, preserving its contents exactly. " +
		"Always use this for renames instead of a shell command or read_file+write_file."
}
func (MoveFile) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"from": {"type": "string"},
			"to": {"type": "string"}
		},
		"required": ["from", "to"]
	}`)
}

func (t MoveFile) Run(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}

	src, err := t.WS.Resolve(in.From)
	if err != nil {
		return "", err
	}
	dst, err := t.WS.Resolve(in.To)
	if err != nil {
		return "", err
	}

	if _, err := os.Stat(src); err != nil {
		return "", fmt.Errorf("source not found: %w", err)
	}
	if _, err := os.Stat(dst); err == nil {
		return "", fmt.Errorf("destination %s already exists — move would overwrite it", in.To)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}

	if err := os.Rename(src, dst); err == nil {
		return "ok", nil
	}

	// os.Rename can fail across filesystem boundaries. Fall back to an
	// explicit copy-then-delete — and if any step of that fails, return a
	// loud error rather than risk a silently empty or partial destination.
	data, err := os.ReadFile(src)
	if err != nil {
		return "", fmt.Errorf("read source: %w", err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return "", fmt.Errorf("write destination: %w", err)
	}
	if err := os.Remove(src); err != nil {
		return "", fmt.Errorf("copied to %s but failed to remove original %s: %w", in.To, in.From, err)
	}
	return "ok", nil
}
