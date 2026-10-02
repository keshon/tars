package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Spill writes full tool output to disk and returns its path. Best-effort:
// any failure returns "" and the caller keeps the truncated inline result.
// root is the workspace root; files land under .agent/output so they never
// pollute the workspace itself.
func Spill(root, tool string, content string) string {
	if root == "" || content == "" {
		return ""
	}
	dir := filepath.Join(root, ".agent", "output")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	name := fmt.Sprintf("%s-%d.log", tool, time.Now().UnixNano())
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		return ""
	}
	return p
}
