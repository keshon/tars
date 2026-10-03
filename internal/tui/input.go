package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
)

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
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

// inputEmpty reports whether the answer box holds no text.
// Quit-on-a-letter keys consult it so typing can never exit the TUI.
func (m *model) inputEmpty() bool {
	return strings.TrimSpace(m.input.Value()) == ""
}
