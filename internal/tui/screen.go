package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"charm.land/lipgloss/v2"
)

// View assembles the four regions: header bar, transcript viewport,
// one-line status, fixed-budget bottom bar. Block building lives in
// transcript.go, status text in status.go — this function only stacks.
func (m *model) screenContent() string {
	if !m.ready {
		return "starting..."
	}
	topRule, bottomRule, spacer := m.dividerLine(), m.dividerLine(), ""
	if m.sidebarVisible() {
		column, width := navigatorWidth+1, m.termW
		topRule = m.paneRule(column, "SESSIONS", m.navFocused) + m.styles.dim.Render("┬") + m.paneRule(width-column-1, "CHAT", !m.navFocused)
		bottomRule = m.styles.dim.Render(strings.Repeat("─", column) + "┴" + strings.Repeat("─", width-column-1))
		spacer = m.sidebarDetails() + " " + m.styles.dim.Render("│")
	}
	body := m.vp.View()
	if m.state == stPermission || m.state == stAsk {
		body = m.gateVP.View()
	}
	if m.sidebarVisible() {
		borderStyle := m.styles.dim
		divider := strings.Repeat(borderStyle.Render(" │ ")+"\n", max(m.vp.Height()-1, 0)) + borderStyle.Render("─┤ ")
		body = lipgloss.JoinHorizontal(lipgloss.Top, m.sidebarView(), divider, body)
	}
	if m.dialog != nil {
		body = m.dialogView()
	} else if m.sessions != nil {
		body = m.sessionsView()
	}
	body = cellFrame(body, max(m.termW, 0), m.vp.Height())
	var bottom string
	if m.dialog != nil {
		bottom = m.styles.dim.Render("Draft preserved")
	} else if m.sessions != nil {
		// The screen owns its keys, so it owns the bottom bar too:
		// leaving the chat input visible underneath suggests typing
		// works there (it doesn't — every key belongs to the list)
		// and its stale height adds phantom lines.
		bottom = m.sessionsBar()
	} else {
		switch m.state {
		case stPermission:
			bottom = m.gateBar()
		case stAsk:
			bottom = m.input.View()
		case stDone:
			// No hint line: its keys moved right into the status row,
			// the freed line belongs to the transcript.
			if m.runErr != nil {
				bottom = m.styles.err.Render("error: "+truncate(m.runErr.Error(), 240)) + "\n" + m.input.View()
			} else {
				bottom = m.input.View()
			}
		default:
			// Blocked input, same box: the zone stays status + input
			// in every state, so layout never jumps when a run
			// starts or lands. Keys route to the viewport here;
			// the blur below hides the caret.
			bottom = m.input.View()
		}
	}
	if m.queued != "" && m.sessions == nil && m.dialog == nil && m.state != stPermission && m.state != stAsk {
		bottom = cellLine("Queued  Ctrl+E edit  Ctrl+X cancel: "+m.queued, max(m.termW, 0)) + "\n" + bottom
	}
	if m.actionBarVisible() {
		bottom += "\n" + m.actionBar()
	}
	// Fill the terminal exactly; the renderer owns cursor placement and wrapping.
	return cellFrame(m.headerLine()+"\n"+m.identityLine()+"\n"+topRule+"\n"+body+"\n"+spacer+"\n"+bottomRule+"\n"+m.statusLine()+"\n"+bottom, max(m.termW, 0), max(m.termH, 0))
}

func (m *model) View() tea.View {
	v := tea.NewView(m.screenContent())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	if !m.ready || m.dialog != nil {
		return v
	}
	if m.sessions != nil {
		if m.sessions.mode == sessRename {
			v.Cursor = m.note.Cursor()
			if v.Cursor != nil {
				width := m.termW
				if width >= 90 {
					width = min(44, (width-3)/2)
				}
				v.Cursor.Y += 3 + lipgloss.Height(m.browserList(width)) - 1
			}
		} else if m.sessions.mode == sessList {
			v.Cursor = m.sessions.filter.Cursor()
			if v.Cursor != nil {
				v.Cursor.Y += 4
			}
		}
	} else if m.state == stPermission {
		if m.gstage == gsReject {
			v.Cursor = m.note.Cursor()
			if v.Cursor != nil {
				v.Cursor.Y += m.termH - m.actionBarRows() - 1
			}
		}
	} else if !m.navFocused {
		v.Cursor = m.input.Cursor()
		if v.Cursor != nil {
			v.Cursor.Y += m.termH - m.actionBarRows() - m.input.Height()
		}
	}
	return v
}
