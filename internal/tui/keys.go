package tui

import (
	tea "charm.land/bubbletea/v2"
	"strings"
)

func (m *model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	defer m.syncFocus()
	if msg.String() == "ctrl+q" {
		return m.requestQuit()
	}
	switch msg.String() {
	case "ctrl+p":
		return m.footerKey(tea.KeyPressMsg{Code: tea.KeyF2})
	case "ctrl+n":
		return m.footerKey(tea.KeyPressMsg{Code: tea.KeyF4})
	case "ctrl+b":
		return m.footerKey(tea.KeyPressMsg{Code: tea.KeyF6})
	}
	if m.activeInputOwner() == focusHeader {
		return m.headerKey(msg)
	}
	switch msg.String() {
	case "f1", "f2", "f3", "f4", "f5", "f6", "f7", "f8", "f9", "f10":
		return m.footerKey(msg)
	}
	if msg.String() == "ctrl+q" || msg.String() == "ctrl+c" {
		m.quit = true
		m.cancel()
		return m, tea.Quit
	}
	if m.state == stPermission || m.state == stAsk {
		if msg.String() == "pgup" || msg.String() == "pgdown" || msg.String() == "ctrl+end" {
			var cmd tea.Cmd
			if msg.String() == "ctrl+end" {
				m.gateVP.GotoBottom()
				return m, nil
			}
			m.gateVP, cmd = m.gateVP.Update(msg)
			return m, cmd
		}
	}
	if m.activeInputOwner() == focusInput {
		switch msg.String() {
		case "ctrl+end":
			m.vp.GotoBottom()
			m.follow = true
			return m, nil
		case "ctrl+u":
			m.input.SetValue("")
			m.fitInput()
			m.fitBottom()
			return m, nil
		case "ctrl+x":
			if m.queued != "" {
				m.queued = ""
				m.fitBottom()
				return m, nil
			}
		case "ctrl+e":
			if m.queued != "" && m.state != stAsk {
				if m.input.Value() != "" {
					m.appendBlock(markerBlock("Keep or clear the current draft before editing the queue."))
					return m, nil
				}
				m.input.SetValue(m.queued)
				m.queued = ""
				m.fitInput()
				m.fitBottom()
				return m, m.input.Focus()
			}
		}
	}
	m.syncSuggestions()
	commandSelected := len(m.suggestions.items) > 0 && strings.HasPrefix(m.suggestions.items[m.suggestions.selected].value, "/")
	if m.suggestionKey(msg) {
		if msg.String() != "enter" || !commandSelected {
			return m, nil
		}
		if m.state == stDone {
			return m.followUp()
		}
		// During a run, queue the completed command through the normal Enter path.
	}
	if m.activeInputOwner() == focusInput && (m.state == stDone || m.state == stAsk || m.state == stRunning) && msg.Mod.Contains(tea.ModAlt) && (msg.Code == tea.KeyUp || msg.Code == tea.KeyDown) {
		m.historyWalk(msg.Code == tea.KeyUp)
		m.fitInput()
		m.fitBottom()
		return m, nil
	}
	if m.state != stPermission && m.state != stAsk && m.sessions == nil && m.dialog == nil {
		switch msg.String() {
		case "ctrl+p":
			m.openSessions()
			return m, nil
		case "ctrl+b":
			m.sidebarHidden = !m.sidebarHidden
			m.fitBottom()
			m.refreshContent()
			return m, nil
		case "tab", "shift+tab":
			if m.sidebarVisible() {
				m.navFocused = !m.navFocused
				if m.navFocused {
					m.input.Blur()
				} else {
					return m, m.input.Focus()
				}
				return m, nil
			}
		case "ctrl+n":
			if m.state == stDone {
				m.navFocused = false
				m.newChat()
				return m, m.input.Focus()
			}
			return m, nil
		}
		if m.navFocused {
			return m.navigatorKey(msg)
		}
	}
	if m.sessions != nil && m.state != stPermission {
		// The sessions screen owns its keys like a dialog — except
		// over a permission gate, which is always on top: its y/n/a
		// answers must never land in a list.
		return m.sessionsKey(msg)
	}
	if m.dialog != nil {
		// Takeover: the dialog eats every key but close and quit so
		// typing can neither reach the input nor toggle state behind it.
		// Close is esc/enter only: "q" must stay typable for dialogs
		// with inputs tomorrow.
		switch msg.String() {
		case "esc", "enter":
			m.closeDialog()
			return m, nil
		case "ctrl+c":
			m.quit = true
			m.cancel()
			return m, tea.Quit
		}
		if m.dialog.help {
			switch msg.String() {
			case "tab", "right":
				m.dialog.page = (m.dialog.page + 1) % len(helpSections())
				m.dialog.offset = 0
			case "shift+tab", "left":
				m.dialog.page = (m.dialog.page + len(helpSections()) - 1) % len(helpSections())
				m.dialog.offset = 0
			case "down":
				m.dialog.offset++
			case "up":
				m.dialog.offset = max(m.dialog.offset-1, 0)
			case "pgdown":
				m.dialog.offset += max(m.vp.Height()/2, 1)
			case "pgup":
				m.dialog.offset = max(m.dialog.offset-max(m.vp.Height()/2, 1), 0)
			}
		}
		return m, nil
	}
	if msg.String() == "ctrl+g" {
		return m.footerKey(tea.KeyPressMsg{Code: tea.KeyF3})
	}

	switch m.state {
	case stPermission:
		return m.gateKey(msg)
	case stAsk:
		switch msg.String() {
		case "esc":
			if answer := m.input.Value(); answer != "" {
				if m.gateDraft != "" {
					answer = m.gateDraft + "\n" + answer
				}
				m.input.SetValue(answer)
				m.gateDraft = ""
				m.fitInput()
			}
			m.interrupted = true
			m.cancel()
			m.state = stStopping
			m.fitBottom()
			return m, nil
		case "pgup", "pgdown":
			// Paging scrolls the transcript in every state; the
			// input keeps arrows and typing only.
			return m.scrollViewport(msg)
		case "enter":
			m.pushHistory(strings.TrimSpace(m.input.Value()))
			_ = m.hub.Respond(m.input.Value())
			m.state = stRunning
			return m, nil
		case "ctrl+c", "ctrl+q":
			m.quit = true
			m.cancel()
			return m, tea.Quit

		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.fitInput()
		m.fitBottom()
		return m, cmd
	case stDone:
		switch msg.String() {
		case "pgup", "pgdown":
			// Same routing as ask: paging belongs to the transcript.
			return m.scrollViewport(msg)
		case "enter":
			return m.followUp()
		case "esc":
			return m, nil
		case "ctrl+c", "ctrl+q":
			// Quit lives on ctrl+q (and ctrl+c): a letter key must
			// never quit, or words starting with q ("queen") become
			// untypable on an empty box. Empty+enter still quits.
			m.quit = true
			m.cancel()
			return m, tea.Quit

		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.fitInput()
		m.fitBottom()
		return m, cmd
	default:
		switch msg.String() {
		case "ctrl+c", "ctrl+q":
			m.quit = true
			m.cancel()
			return m, tea.Quit
		case "esc":
			if m.state != stStopping {
				m.interrupted = true
				m.cancel()
				m.state = stStopping
				m.fitBottom()
			}
			return m, nil
		case "enter":
			if m.state == stStopping {
				return m, nil
			}
			text := strings.TrimSpace(m.input.Value())
			if text == "" {
				return m, nil
			}
			if m.queued != "" {
				m.appendBlock(markerBlock("a follow-up is already queued"))
				return m, nil
			}
			m.queued = text
			m.input.SetValue("")
			m.appendBlock(markerBlock("queued follow-up: " + truncate(text, 120)))
			m.fitInput()
			m.fitBottom()
			return m, nil
		case "pgup", "pgdown":
			return m.scrollViewport(msg)
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.fitInput()
		m.fitBottom()
		return m, cmd
	}
}

func isMouseClick(msg tea.MouseMsg) bool { _, ok := msg.(tea.MouseClickMsg); return ok }
