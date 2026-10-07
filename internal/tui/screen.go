package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"charm.land/lipgloss/v2"
)

// screenContent keeps the composer inside the conversation column and the
// function-key bar outside both panes.
func (m *model) screenContent() string {
	if !m.ready {
		return "starting..."
	}
	topRule := m.dividerLine()
	if m.sidebarVisible() {
		column := navigatorWidth + 1
		topRule = m.paneRule(column, "SESSIONS", m.navFocused) + m.styles.dim.Render("┬") + m.paneRule(m.termW-column-1, "CHAT", !m.navFocused)
	}
	popup := len(m.suggestions.items) > 0 && m.vp.Height() >= 4 && m.vp.Width() >= 24
	body := m.vp.View()
	gap := "\n"
	if popup {
		body += "\n"
		gap = ""
	}
	body = m.suggestionView(body)
	if m.state == stPermission || m.state == stAsk {
		body = m.gateVP.View()
	}
	if m.dialog != nil {
		body = m.dialogView()
	}
	bodyHeight := m.vp.Height()
	if popup {
		bodyHeight++
	}
	body = cellFrame(body, m.vp.Width(), bodyHeight)
	var bottom string
	switch {
	case m.dialog != nil:
		bottom = m.styles.dim.Render("Draft preserved")
	case m.state == stPermission:
		bottom = m.gateBar()
	default:
		bottom = m.input.View()
		if m.state == stDone && m.runErr != nil {
			bottom = m.styles.err.Render("error: "+truncate(m.runErr.Error(), 240)) + "\n" + bottom
		}
	}
	if m.queued != "" && m.dialog == nil && m.state != stPermission && m.state != stAsk {
		bottom = cellLine("Queued  Ctrl+E edit  Ctrl+X cancel: "+m.queued, m.vp.Width()) + "\n" + bottom
	}
	bottom = cellFrame(bottom, m.vp.Width(), lipgloss.Height(bottom))
	conversation := body + gap + "\n" + m.styles.dim.Render(strings.Repeat("─", m.vp.Width())) + "\n" + cellLine(m.statusLine(), m.vp.Width()) + "\n" + bottom
	conversation = cellFrame(conversation, m.vp.Width(), m.paneHeight())
	if m.sidebarVisible() {
		divider := strings.Repeat(m.styles.dim.Render(" │ ")+"\n", m.paneHeight()-1) + m.styles.dim.Render(" │ ")
		// Connect only the conversation composer rule to the vertical divider.
		rows := strings.Split(divider, "\n")
		ruleRow := bodyHeight
		if !popup {
			ruleRow++
		}
		rows[ruleRow] = m.styles.dim.Render(" ├─")
		divider = strings.Join(rows, "\n")
		conversation = lipgloss.JoinHorizontal(lipgloss.Top, m.sidebarView(), divider, conversation)
	}
	content := m.headerLine() + "\n" + m.identityLine() + "\n" + topRule + "\n" + conversation
	if m.actionBarVisible() {
		rule := m.dividerLine()
		if m.sidebarVisible() {
			column := navigatorWidth + 1
			rule = m.styles.dim.Render(strings.Repeat("─", column) + "┴" + strings.Repeat("─", m.termW-column-1))
		}
		content += "\n" + rule + "\n" + m.actionBar()
	}
	content = cellFrame(content, max(m.termW, 0), max(m.termH, 0))
	if m.sessions != nil {
		content = m.overlaySearch(content)
	}
	if m.headerMenu != "" {
		content = m.overlayHeader(content)
	}
	return content
}

func (m *model) View() tea.View {
	v := tea.NewView(m.screenContent())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	if !m.ready || m.dialog != nil || m.headerFocused {
		return v
	}
	if m.sessions != nil {
		g := m.searchGeometry()
		if m.sessions.mode == sessRename {
			v.Cursor = m.note.Cursor()
			if v.Cursor != nil {
				v.Cursor.X += g.x + 1
				v.Cursor.Y += g.noteRow
			}
		} else if m.sessions.mode == sessList {
			v.Cursor = m.sessions.filter.Cursor()
			if v.Cursor != nil {
				v.Cursor.X += g.x + 1
				v.Cursor.Y += g.y + 1
			}
		}
	} else if m.state == stPermission {
		if m.gstage == gsReject {
			v.Cursor = m.note.Cursor()
			if v.Cursor != nil {
				v.Cursor.X += m.conversationOffset()
				v.Cursor.Y += m.termH - m.footerRows() - 1
			}
		}
	} else if !m.navFocused {
		v.Cursor = m.input.Cursor()
		if v.Cursor != nil {
			v.Cursor.X += m.conversationOffset()
			v.Cursor.Y += m.termH - m.footerRows() - m.input.Height()
		}
	}
	return v
}
