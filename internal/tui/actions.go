package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type footerAction struct {
	key                rune
	mod                tea.KeyMod
	name, label, short string
	enabled            bool
}

type footerCell struct {
	action          footerAction
	row, start, end int
}

func (m *model) legacyFooterActions() []footerAction {
	gated := m.state == stPermission || m.state == stAsk
	chat := !gated && m.dialog == nil && m.sessions == nil && !m.header.focused
	idle := chat && m.state == stDone
	return []footerAction{
		{tea.KeyF1, 0, "F1", "Help", "Help", !gated && m.sessions == nil && !m.header.focused},
		{tea.KeyF2, 0, "F2", "Search", "Search", !gated && m.dialog == nil && !m.header.focused},
		{tea.KeyF3, 0, "F3", "Details", "View", chat},
		{tea.KeyF4, 0, "F4", "New", "New", idle},
		{tea.KeyF5, 0, "F5", "Mode", "Mode", idle && !m.mission},
		{tea.KeyF6, 0, "F6", "Sidebar", "Pane", chat && m.termW >= navigatorMinWidth},
		{tea.KeyF7, 0, "F7", "Latest", "End", chat},
		{tea.KeyF8, 0, "F8", "", "", false},
		{tea.KeyF9, 0, "F9", "Menu", "Menu", !gated && m.dialog == nil && m.sessions == nil},
		{tea.KeyF10, 0, "F10", "Quit", "Quit", true},
	}
}

// The first three actions stay in place; the middle pair follows input ownership.
func (m *model) footerActions() []footerAction {
	legacy := m.legacyFooterActions()
	actions := []footerAction{
		{tea.KeyF1, 0, "F1", "Help", "Help", legacy[0].enabled},
		{'p', tea.ModCtrl, "Ctrl+P", "Search", "Find", legacy[1].enabled},
		{'n', tea.ModCtrl, "Ctrl+N", "New", "New", legacy[3].enabled},
	}
	switch m.activeInputOwner() {
	case focusNavigator, focusSearch:
		editable := m.state == stDone && (m.sessions == nil || m.sessions.mode == sessList)
		actions = append(actions, footerAction{'r', tea.ModCtrl, "Ctrl+R", "Rename", "Name", editable}, footerAction{'d', tea.ModCtrl, "Ctrl+D", "Delete", "Del", editable})
	case focusInput:
		if m.state == stRunning || !m.follow {
			actions = append(actions, footerAction{tea.KeyEscape, 0, "Esc", "Stop", "Stop", m.state == stRunning}, footerAction{tea.KeyEnd, tea.ModCtrl, "Ctrl+End", "Latest", "End", true})
		} else {
			actions = append(actions, footerAction{tea.KeyEnter, 0, "Enter", "Send", "Send", m.state == stDone || m.state == stAsk}, footerAction{'o', tea.ModCtrl, "Ctrl+O", "Newline", "Line", true})
		}
	default:
		actions = append(actions, footerAction{tea.KeyEscape, 0, "Esc", "Back", "Back", m.state != stPermission}, footerAction{tea.KeyEnter, 0, "Enter", "Choose", "OK", m.activeInputOwner() == focusHeader || m.activeInputOwner() == focusNote})
	}
	if m.activeInputOwner() == focusInput && m.state == stDone && !m.follow {
		actions[3] = footerAction{tea.KeyEnter, 0, "Enter", "Send", "Send", true}
	}
	details := "Details"
	if !m.compact {
		details = "Less"
	}
	return append(actions,
		footerAction{'g', tea.ModCtrl, "Ctrl+G", details, details, legacy[2].enabled},
		footerAction{'b', tea.ModCtrl, "Ctrl+B", "Sessions", "Pane", legacy[5].enabled},
		footerAction{'q', tea.ModCtrl, "Ctrl+Q", "Quit", "Quit", true})
}

func (m *model) actionBarVisible() bool { return m.termW >= 40 && m.termH >= 14 }
func (m *model) actionBarRows() int {
	if !m.actionBarVisible() {
		return 0
	}
	if m.termW < 120 {
		return 2
	}
	return 1
}

// footerRows includes the separator above the global action bar.
func (m *model) footerRows() int {
	if !m.actionBarVisible() {
		return 0
	}
	return m.actionBarRows() + 1
}

// Rendering and mouse clicks share these cell boundaries, including gaps.
func (m *model) footerCells() []footerCell {
	actions := m.footerActions()
	columns := (len(actions) + max(m.actionBarRows(), 1) - 1) / max(m.actionBarRows(), 1)
	width := max(m.termW, 0)
	cells := make([]footerCell, len(actions))
	for i, action := range actions {
		row, column := i/columns, i%columns
		count := min(columns, len(actions)-row*columns)
		contentWidth := width - (count - 1)
		start := column*contentWidth/count + column
		end := (column+1)*contentWidth/count + column
		cells[i] = footerCell{action, row, start, end}
	}
	return cells
}

func (m *model) actionBar() string {
	var rows []string
	row := ""
	current := 0
	for _, cell := range m.footerCells() {
		if cell.row != current {
			rows = append(rows, row)
			row = ""
			current = cell.row
		}
		if row != "" {
			row += " "
		}
		label := cell.action.label
		number := cell.action.name
		if cell.end-cell.start < 14 && strings.HasPrefix(number, "Ctrl+") {
			number = "^" + strings.TrimPrefix(number, "Ctrl+")
		}
		if cell.end-cell.start < len(number)+1+len(label) {
			label = cell.action.short
		}
		if cell.action.key == tea.KeyF3 && !m.compact {
			label = "Less"
		}
		numberStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(colorDisabledText)).Background(lipgloss.Color(colorDisabledNumber))
		labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(colorDisabledText)).Background(lipgloss.Color(colorDisabledLabel))
		if cell.action.enabled {
			numberStyle = numberStyle.Foreground(lipgloss.Color(colorNumberText)).Background(lipgloss.Color(colorNumberBackground)).Bold(true)
			labelStyle = labelStyle.Foreground(lipgloss.Color(colorSelectedText)).Background(lipgloss.Color(colorActionBackground))
		}
		row += numberStyle.Render(number) + labelStyle.Render(cellLine(" "+label, cell.end-cell.start-len(number)))
	}
	return strings.Join(append(rows, row), "\n")
}

func (m *model) footerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	for _, action := range m.legacyFooterActions() {
		if action.key == msg.Code && !action.enabled {
			return m, nil
		}
	}
	switch msg.Code {
	case tea.KeyF1:
		if m.dialog != nil {
			m.closeDialog()
		} else {
			m.openHelp()
		}
	case tea.KeyF2:
		if m.sessions != nil {
			m.closeSessions()
		} else {
			m.openSessions()
		}
	case tea.KeyF3:
		m.compact = !m.compact
		m.refreshContent()
	case tea.KeyF4:
		m.navFocused = false
		m.newChat()
		return m, m.input.Focus()
	case tea.KeyF5:
		mode := "plan"
		if m.plan {
			mode = "act"
		}
		return m.command("/mode " + mode)
	case tea.KeyF6:
		m.sidebarHidden = !m.sidebarHidden
		m.fitBottom()
		m.refreshContent()
	case tea.KeyF7:
		m.vp.GotoBottom()
		m.follow = true
	case tea.KeyF9:
		m.header.focused = !m.header.focused
		m.header.menu = ""
	case tea.KeyF10:
		return m.requestQuit()
	}
	return m, nil
}

// paneRule labels the pane in its border without spending a content row.
func (m *model) paneRule(width int, label string, focused bool) string {
	label = " " + label + " "
	if focused {
		label = " ▸" + label
	}
	labelStyle := m.styles.dim
	if focused {
		labelStyle = m.styles.hunk.Bold(true)
	}
	label = cellLine(label, min(max(width-1, 0), lipgloss.Width(label)))
	return m.styles.dim.Render("─") + labelStyle.Render(label) + m.styles.dim.Render(strings.Repeat("─", max(width-1-lipgloss.Width(label), 0)))
}

// A normal quit first stops an active run. Ctrl+C remains the immediate exit.
func (m *model) requestQuit() (tea.Model, tea.Cmd) {
	if m.state != stDone {
		if m.state != stStopping {
			m.interrupted = true
			m.cancel()
			m.state = stStopping
			m.appendBlock(markerBlock("Stopping run. Ctrl+Q quits once it has stopped; Ctrl+C exits immediately."))
			m.fitBottom()
		}
		return m, nil
	}
	m.quit = true
	m.cancel()
	return m, tea.Quit
}
