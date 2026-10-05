package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// command handles local slash commands. Anything unrecognized is
// reported, never sent to the model — a typo must not become a task.
func (m *model) command(text string) (tea.Model, tea.Cmd) {
	name, arg, _ := strings.Cut(strings.TrimPrefix(text, "/"), " ")
	m.pushHistory(text)
	switch strings.ToLower(name) {
	case "q", "quit":
		m.quit = true
		m.cancel()
		return m, tea.Quit
	case "help":
		// Reference goes to history, not a modal: opencode centers
		// dialogs, pi embeds panels inline, and for read-only text
		// inline wins (scrollback keeps it, nothing to dismiss).
		// Modals stay reserved for interactive pickers (P10-5).
		// Bare (no per-line gutter): section titles carry identity.
		hb := markerBlock(strings.Join(renderHelp(), "\n"))
		hb.bare = true
		m.appendBlock(hb)
		return m, nil
	case "new":
		task := strings.TrimSpace(arg)
		if task == "" {
			// Bare /new resets to the empty-chat state (same as
			// opening with no initial task): no session, clean
			// transcript, the next submitted line starts a fresh
			// run. The task text is the session title, so there is
			// nothing sensible to derive one from yet.
			m.newChat()
			return m, m.input.Focus()
		}
		if err := m.startFresh(task); err != nil {
			m.appendBlock(errorBlock("cannot start: " + err.Error()))
			return m, nil
		}
		return m, nil
	case "status":
		sb := markerBlock(strings.Join(m.statusLines(), "\n"))
		sb.bare = true
		m.appendBlock(sb)
		return m, nil
	case "retry":
		if m.runErr == nil || m.retryRun == nil {
			m.appendBlock(markerBlock("nothing to retry: last run did not fail"))
			return m, nil
		}
		m.appendBlock(markerBlock("— retrying —"))
		m.startRun(m.retryRun)
		return m, nil
	case "sessions":
		m.openSessions()
		return m, nil
	case "compact":
		return m.compactNow()
	default:
		m.appendBlock(errorBlock("unknown command " + text + " (try /help)"))
		return m, nil
	}
}
