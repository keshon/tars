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
	return body + "\n\n" + m.dividerLine() + "\n" + m.statusLine() + "\n" + bottom
}
