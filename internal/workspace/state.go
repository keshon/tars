package workspace

import (
	"os"
	"path/filepath"
	"strings"
)

// StateDirName is the harness-owned state directory inside a workspace:
// task snapshots, mission ledgers, spilled tool output. Single copy so
// the CLI, serve mode, TUI, and eval namespace the same layout.
const StateDirName = ".tars"

// TaskDir namespaces one task per directory: <statedir>/tasks/<id>.
// Relative like every other task path the CLI prints for -resume.
func TaskDir(taskID string) string {
	return filepath.Join(StateDirName, "tasks", taskID)
}

// SessionTitleFile stores a session's display title inside its own
// dir (pi's session_info pattern): written once at creation, rewritten
// on rename. It travels with the session — delete removes it, so titles
// can never orphan or drift like a separate index.
const SessionTitleFile = "title"

// titleCap bounds titles: display-ready, one line.
const titleCap = 60

// TitleLine shapes raw text into a title: first line, trimmed, capped.
func TitleLine(text string) string {
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	line := strings.TrimSpace(strings.ReplaceAll(text, "\r", ""))
	if r := []rune(line); len(r) > titleCap {
		line = string(r[:titleCap])
	}
	return line
}

// WriteSessionTitle stores the title (empty clears back to derived
// text). Best-effort: titles are display metadata, never load-bearing.
func WriteSessionTitle(dir, title string) error {
	path := filepath.Join(dir, SessionTitleFile)
	if title == "" {
		err := os.Remove(path)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return os.WriteFile(path, []byte(title+"\n"), 0o644)
}

// ReadSessionTitle returns the stored title, "" when absent.
func ReadSessionTitle(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, SessionTitleFile))
	if err != nil {
		return ""
	}
	return TitleLine(string(raw))
}
