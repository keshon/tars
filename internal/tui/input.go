package tui

import (
	"github.com/charmbracelet/bubbles/textarea"
)

// truncate shortens display strings with a marker (R5). Rune-based:
// a byte slice can split multi-byte UTF-8 and emit invalid output.
func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// newInput builds the answer box: multiline, capped at a few rows.
// Enter submits (handled by the model, never reaching the widget);
// ctrl+o inserts a newline instead. Shift+enter would be the familiar
// spelling, but Windows consoles deliver it indistinguishably from
// enter — ctrl+o is unambiguous everywhere.
func newInput() textarea.Model {
	ta := textarea.New()
	ta.Prompt = "> "
	ta.MaxHeight = 6
	ta.KeyMap.InsertNewline.SetKeys("ctrl+o")
	return ta
}
