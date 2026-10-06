package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// View assembles the four regions: header bar, transcript viewport,
// one-line status, fixed-budget bottom bar. Block building lives in
// transcript.go, status text in status.go — this function only stacks.
func (m *model) View() string {
	if !m.ready {
		return "starting..."
	}
	topRule, bottomRule, spacer := m.dividerLine(), m.dividerLine(), ""
	if m.sidebarVisible() {
		column, width := navigatorWidth+1, m.termW-1
		topRule = m.styles.dim.Render(strings.Repeat("─", column) + "┬" + strings.Repeat("─", width-column-1))
		bottomRule = m.styles.dim.Render(strings.Repeat("─", column) + "┴" + strings.Repeat("─", width-column-1))
		spacer = strings.Repeat(" ", column) + m.styles.dim.Render("│")
	}
	body := m.vp.View()
	if m.state == stPermission || m.state == stAsk {
		body = m.gateVP.View()
	}
	if m.sidebarVisible() {
		divider := strings.Repeat(m.styles.dim.Render(" \u2502 ")+"\n", max(m.vp.Height-1, 0)) + m.styles.dim.Render(" \u2502 ")
		body = lipgloss.JoinHorizontal(lipgloss.Top, m.sidebarView(), divider, body)
	}
	if m.dialog != nil {
		body = m.dialogView()
	} else if m.sessions != nil {
		body = m.sessionsView()
	}
	var bottom string
	if m.sessions != nil {
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
	if m.queued != "" && m.sessions == nil && m.state != stPermission && m.state != stAsk {
		bottom = cellLine("Queued  Ctrl+E edit  Ctrl+X cancel: "+m.queued, max(m.termW-1, 0)) + "\n" + bottom
	}
	// Leave the bottom row and rightmost column unused. Writing the last
	// terminal cell can trigger automatic wrapping and scroll the whole
	// screen on Windows consoles, even with the alternate screen enabled.
	return cellFrame(m.headerLine()+"\n"+m.identityLine()+"\n"+topRule+"\n"+body+"\n"+spacer+"\n"+bottomRule+"\n"+m.statusLine()+"\n"+bottom, max(m.termW-1, 0), max(m.termH-1, 0))
}
