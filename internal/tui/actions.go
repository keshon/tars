package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type footerAction struct {
	key                rune
	name, label, short string
	enabled            bool
}

type footerCell struct {
	action          footerAction
	row, start, end int
}

func (m *model) footerActions() []footerAction {
	gated := m.state == stPermission || m.state == stAsk
	chat := !gated && m.dialog == nil && m.sessions == nil
	idle := chat && m.state == stDone
	return []footerAction{
		{tea.KeyF1, "F1", "Help", "Help", !gated && m.sessions == nil},
		{tea.KeyF2, "F2", "Sessions", "Sessions", !gated && m.dialog == nil},
		{tea.KeyF3, "F3", "Details", "View", chat},
		{tea.KeyF4, "F4", "New", "New", idle},
		{tea.KeyF5, "F5", "Mode", "Mode", idle && !m.mission},
		{tea.KeyF6, "F6", "Sidebar", "Pane", chat && m.termW >= navigatorMinWidth},
		{tea.KeyF7, "F7", "Latest", "End", chat},
		{tea.KeyF8, "F8", "", "", false},
		{tea.KeyF9, "F9", "", "", false},
		{tea.KeyF10, "F10", "Quit", "Quit", true},
	}
}

func (m *model) actionBarVisible() bool { return m.termW >= 40 && m.termH >= 14 }
func (m *model) actionBarRows() int {
	if !m.actionBarVisible() {
		return 0
	}
	if m.termW < 80 {
		return 2
	}
	return 1
}

// Rendering and mouse clicks share these cell boundaries, including gaps.
func (m *model) footerCells() []footerCell {
	actions := m.footerActions()
	columns := len(actions)
	if m.actionBarRows() == 2 {
		columns /= 2
	}
	width := max(m.termW, 0)
	contentWidth := width - (columns - 1)
	cells := make([]footerCell, len(actions))
	for i, action := range actions {
		column := i % columns
		start := column*contentWidth/columns + column
		end := (column+1)*contentWidth/columns + column
		cells[i] = footerCell{action, i / columns, start, end}
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
		number := strings.TrimPrefix(cell.action.name, "F")
		if cell.end-cell.start < len(number)+1+len(label) {
			label = cell.action.short
		}
		if cell.action.key == tea.KeyF3 && !m.compact {
			label = "Less"
		}
		numberStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#77818E")).Background(lipgloss.Color("#20262E"))
		labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#77818E")).Background(lipgloss.Color("#282E36"))
		if cell.action.enabled {
			numberStyle = numberStyle.Foreground(lipgloss.Color("#DCE5EF")).Background(lipgloss.Color("#303944")).Bold(true)
			labelStyle = labelStyle.Foreground(lipgloss.Color("#F1F4F8")).Background(lipgloss.Color("#526F91"))
		}
		row += numberStyle.Render(number) + labelStyle.Render(cellLine(" "+label, cell.end-cell.start-len(number)))
	}
	return strings.Join(append(rows, row), "\n")
}

func (m *model) footerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	for _, action := range m.footerActions() {
		if action.key == msg.Code && !action.enabled {
			return m, nil
		}
	}
	switch msg.Code {
	case tea.KeyF1:
		if m.dialog != nil {
			m.closeDialog()
		} else {
			m.openDialog("Help", []string{
				"F1 Help   F2 Sessions   F3 Details   F4 New",
				"F5 Plan/Act mode   F6 Sidebar   F7 Latest",
				"Enter send / queue / answer   Shift+Enter newline",
				"Up/Down edit   Alt+Up/Down input history",
				"Tab switch pane   Esc stop run / go back",
				"Ctrl+E edit queue   Ctrl+X cancel queue",
				"Ctrl+U clear draft   /help full reference",
			})
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
	case tea.KeyF10:
		m.quit = true
		m.cancel()
		return m, tea.Quit
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
