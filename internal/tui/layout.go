package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	navigatorWidth    = 34
	navigatorMinWidth = 110
)

// Terminal cells, rather than byte or rune counts, own the frame geometry.
func cellLine(s string, width int) string {
	width = max(width, 0)
	s = ansi.Truncate(strings.ReplaceAll(s, "\n", " "), width, "…")
	return s + strings.Repeat(" ", max(width-lipgloss.Width(s), 0))
}
func cellFrame(s string, width, height int) string {
	lines := strings.Split(s, "\n")
	out := make([]string, max(height, 0))
	for i := range out {
		if i < len(lines) {
			out[i] = cellLine(lines[i], width)
		} else {
			out[i] = strings.Repeat(" ", max(width, 0))
		}
	}
	return strings.Join(out, "\n")
}
func (m *model) sidebarVisible() bool {
	return !m.sidebarHidden && m.termW >= navigatorMinWidth && m.termH >= 18 &&
		m.dialog == nil
}
func (m *model) fitColumns() {
	width := m.termW
	if m.sidebarVisible() {
		width -= navigatorWidth + 3
	} else {
		if m.navFocused {
			m.input.Focus()
		}
		m.navFocused = false
	}
	m.vp.SetWidth(max(width, 1))
}
func (m *model) identityLine() string {
	identity := m.statusWord()
	if facts := m.runFacts(); facts != "" {
		// Put run progress before model details so it survives narrow terminals.
		identity += "  " + facts + "  " + nonEmpty(m.modelName)
	} else {
		identity += "  " + nonEmpty(m.modelName)
		if m.limit > 0 {
			identity += "  context " + kTokens(m.limit)
		}
	}
	return cellLine(m.styles.dim.Render(m.modeName()+"  ")+cellLine(m.spinner(), 1)+m.styles.dim.Render(" "+identity), max(m.termW, 0))
}
func (m *model) conversationOffset() int {
	if m.sidebarVisible() {
		return navigatorWidth + 3
	}
	return 0
}
func (m *model) paneHeight() int    { return max(m.termH-3-m.footerRows(), 1) }
func (m *model) navigatorRows() int { return max(m.paneHeight()-1, 1) }

func (m *model) sidebarView() string {
	rows := []string{}
	visible := m.navigatorRows()
	m.nav.clamp(visible)
	end := min(m.nav.offset+visible, len(m.nav.entries))
	for i := m.nav.offset; i < end; i++ {
		e := m.nav.entries[i]
		mark := " "
		switch m.entryState(e) {
		case "Working", "Stopping":
			mark = "*"
		case "Needs input":
			mark = "?"
		case "Failed", "Unreadable":
			mark = "!"
		}
		if m.navFocused && i == m.nav.cursor {
			mark = "›"
		} else if sameSession(e.dir, m.stateFile) && mark == " " {
			mark = m.styles.hunk.Render("▌")
		}
		age := strings.TrimSuffix(ageString(e.updated), " ago")
		if age == "just now" {
			age = "now"
		}
		age = cellLine(age, 4)
		line := cellLine(mark+e.title, navigatorWidth-5)
		if m.navFocused && i == m.nav.cursor {
			line = m.styles.hunk.Bold(true).Reverse(true).Render(line)
		}
		rows = append(rows, line+" "+m.styles.dim.Render(age))
	}
	if len(m.nav.entries) == 0 {
		rows = append(rows, m.styles.dim.Render("No saved sessions yet"))
	}
	for len(rows) < m.navigatorRows() {
		rows = append(rows, "")
	}
	rows = append(rows, m.sidebarDetails())
	return cellFrame(strings.Join(rows, "\n"), navigatorWidth, m.paneHeight())
}

func (m *model) sidebarDetails() string {
	if len(m.nav.entries) == 0 {
		return "No session selected"
	}
	m.nav.clamp(m.navigatorRows())
	e := m.nav.entries[m.nav.cursor]
	noun := "messages"
	if e.msgs == 1 {
		noun = "message"
	}
	details := fmt.Sprintf("%s  %d %s", nonEmpty(e.mode), e.msgs, noun)
	if state := m.entryState(e); state != "" {
		details = state + "  " + details
	}
	return m.styles.dim.Render(cellLine(details, navigatorWidth))
}

func (m *model) entryState(e sessionEntry) string {
	if sameSession(e.dir, m.stateFile) {
		switch m.state {
		case stRunning:
			return "Working"
		case stStopping:
			return "Stopping"
		case stAsk, stPermission:
			return "Needs input"
		case stDone:
			if m.runErr != nil {
				return "Failed"
			}
		}
	}
	if e.status != "" && e.status != "Saved" {
		return e.status
	}
	return ""
}
func (m *model) navigatorKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" || msg.String() == "ctrl+q" {
		m.quit = true
		m.cancel()
		return m, tea.Quit
	}
	switch msg.String() {
	case "esc":
		m.navFocused = false
		return m, m.input.Focus()
	case "up", "down", "pgup", "pgdown":
		step := 1
		if msg.String() == "pgup" || msg.String() == "pgdown" {
			step = m.navigatorRows()
		}
		if msg.String() == "up" || msg.String() == "pgup" {
			step = -step
		}
		m.nav.cursor += step
		m.nav.clamp(m.navigatorRows())
	case "enter", "ctrl+r", "ctrl+d":
		if m.state != stDone {
			m.appendBlock(markerBlock("Stop the run before switching sessions."))
			return m, nil
		}
		if len(m.nav.entries) == 0 {
			return m, nil
		}
		dir := m.nav.entries[m.nav.cursor].dir
		m.openSessions()
		for i, e := range m.sessions.entries {
			if e.dir == dir {
				m.sessions.cursor = i
				break
			}
		}
		if msg.String() != "enter" {
			m.sessions.fromNavigator = true
			return m.sessionsKey(msg)
		}
		return m, m.openSelected()
	}
	return m, nil
}
