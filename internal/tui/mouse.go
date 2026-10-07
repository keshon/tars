package tui

import tea "charm.land/bubbletea/v2"

func (m *model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if handled, cmd := m.headerMouse(msg); handled {
		return m, cmd
	}
	if m.actionBarVisible() && msg.Mouse().Y >= m.termH-m.actionBarRows() && msg.Mouse().Y < m.termH && msg.Mouse().Button == tea.MouseLeft && isMouseClick(msg) {
		for _, cell := range m.footerCells() {
			if msg.Mouse().Y == m.termH-m.actionBarRows()+cell.row && msg.Mouse().X >= cell.start && msg.Mouse().X < cell.end {
				return m.footerKey(tea.KeyPressMsg{Code: cell.action.key})
			}
		}
		return m, nil
	}
	if m.state == stPermission || m.state == stAsk {
		if msg.Mouse().X < m.conversationOffset() || msg.Mouse().Y < 3 || msg.Mouse().Y >= 3+m.vp.Height() {
			return m, nil
		}
		var cmd tea.Cmd
		m.gateVP, cmd = m.gateVP.Update(msg)
		return m, cmd
	}
	if m.sessions != nil {
		return m.browserMouse(msg)
	}
	if m.sidebarVisible() && msg.Mouse().X < navigatorWidth && msg.Mouse().Y >= 3 && msg.Mouse().Y < 3+m.paneHeight() {
		if m.state == stPermission || m.state == stAsk {
			return m, nil
		}
		switch msg.Mouse().Button {
		case tea.MouseWheelUp:
			m.nav.cursor--
		case tea.MouseWheelDown:
			m.nav.cursor++
		case tea.MouseLeft:
			if !isMouseClick(msg) {
				return m, nil
			}
			row := msg.Mouse().Y - 3
			if msg.Mouse().Y < 3 || row >= m.navigatorRows() || m.nav.offset+row >= len(m.nav.entries) {
				return m, nil
			}
			m.nav.cursor = m.nav.offset + row
		default:
			return m, nil
		}
		m.nav.clamp(m.navigatorRows())
		m.navFocused = true
		m.input.Blur()
		return m, nil
	}
	if m.dialog == nil && m.sessions == nil && m.sidebarVisible() && msg.Mouse().X >= navigatorWidth+3 && msg.Mouse().Y >= 3 && msg.Mouse().Y < 3+m.paneHeight() && msg.Mouse().Button == tea.MouseLeft && isMouseClick(msg) {
		m.navFocused = false
		return m, m.input.Focus()
	}
	if m.dialog != nil && m.dialog.help {
		switch msg.Mouse().Button {
		case tea.MouseWheelDown:
			m.dialog.offset++
		case tea.MouseWheelUp:
			m.dialog.offset = max(m.dialog.offset-1, 0)
		}
		return m, nil
	}
	// An overlay freezes the background: scroll resumes on close.
	if m.dialog != nil || m.sessions != nil {
		return m, nil
	}
	// The input never consumes mouse messages, so scroll works in
	// every state: wheel in ask/done used to fall through and die.
	return m.scrollViewport(msg)
}
