package tui

import (
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
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
// Shift+Enter inserts a newline; Ctrl+O remains a terminal fallback.
func newInput() textarea.Model {
	ta := textarea.New()
	ta.Prompt = "> "
	ta.SetVirtualCursor(false)
	style := ta.Styles()
	style.Cursor.Shape = tea.CursorUnderline
	ta.SetStyles(style)
	ta.MaxHeight = 6
	ta.DynamicHeight = true
	ta.MaxContentHeight = 4096
	// Line numbers default on in bubbles and render as a phantom "1"
	// in the empty box — it looks like content but submits nothing.
	ta.ShowLineNumbers = false
	ta.KeyMap.InsertNewline.SetKeys("shift+enter", "ctrl+o")
	return ta
}
