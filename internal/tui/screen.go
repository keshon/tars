package tui

// View assembles the three regions: transcript viewport, one-line
// status, fixed-budget bottom bar. Block building lives in
// transcript.go, status text in status.go — this function only stacks.
func (m *model) View() string {
	if !m.ready {
		return "starting..."
	}
	body := m.vp.View()
	if m.dialog != nil {
		body = m.dialogView()
	} else if m.sessions != nil {
		body = m.sessionsView()
	}
	var bottom string
	switch m.state {
	case stPermission:
		bottom = m.gateBar()
	case stAsk:
		bottom = m.input.View()
	case stDone:
		if m.runErr != nil {
			bottom = m.styles.err.Render("error: "+truncate(m.runErr.Error(), 240)) + "\n" + m.input.View()
		} else {
			bottom = m.styles.dim.Render("follow-up · empty + enter to quit · ctrl+q quits · ctrl+g details") + "\n" + m.input.View()
		}
	default:
		bottom = m.styles.dim.Render("working… (q/esc to stop · ctrl+c to abort)")
	}
	return body + "\n" + m.dividerLine() + "\n" + m.statusLine() + "\n" + bottom
}
