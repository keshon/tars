package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Shared by help and command completion.
func commandReference() [][2]string {
	return [][2]string{
		{"/help", "Open this help"},
		{"/sessions", "Browse saved sessions"},
		{"/new [task]", "Start a fresh task; omit task for an empty session"},
		{"/mode plan|act", "Preview changes or execute them"},
		{"/status", "Show current run facts"},
		{"/retry", "Retry the last failed turn"},
		{"/compact", "Shrink this session's history"},
		{"/quit", "Quit"},
	}
}

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
		m.openHelp()
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
		clean, images, err := splitAttachments(m.ws, task)
		if err != nil {
			m.appendBlock(errorBlock(err.Error()))
			return m, nil
		}
		if err := m.startFreshPrompt(clean, task, images); err != nil {
			m.appendBlock(errorBlock("cannot start: " + err.Error()))
			return m, nil
		}
		return m, nil
	case "status":
		sb := markerBlock(strings.Join(m.statusLines(), "\n"))
		sb.bare = true
		m.appendBlock(sb)
		return m, nil
	case "mode":
		mode := strings.ToLower(strings.TrimSpace(arg))
		if mode == "" {
			m.appendBlock(markerBlock("Mode: " + m.modeName() + " · /mode plan previews changes · /mode act executes them"))
			return m, nil
		}
		if m.mission || (mode != "plan" && mode != "act") {
			m.appendBlock(errorBlock("Choose /mode plan or /mode act for a chat."))
			return m, nil
		}
		previous := m.plan
		m.plan = mode == "plan"
		if m.sess != nil {
			sess, err := m.newSession(m.activeSessionTitle(), m.stateFile, nil)
			if err != nil {
				m.plan = previous
				m.appendBlock(errorBlock(err.Error()))
				return m, nil
			}
			if err := m.saveMode(); err != nil {
				m.plan = previous
				m.appendBlock(errorBlock(err.Error()))
				return m, nil
			}
			m.sess = sess
		}
		m.appendBlock(markerBlock("Mode: " + m.modeName()))
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
