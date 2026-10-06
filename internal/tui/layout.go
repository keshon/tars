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
		m.sessions == nil && m.dialog == nil && m.state != stPermission && m.state != stAsk
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
func (m *model) navigatorRows() int { return max(m.vp.Height()-1, 1) }

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
	for len(rows) < m.vp.Height()-1 {
		rows = append(rows, "")
	}
	rows = append(rows, m.styles.dim.Render(strings.Repeat("─", navigatorWidth)))
	return cellFrame(strings.Join(rows, "\n"), navigatorWidth, m.vp.Height())
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
func (m *model) browserList(width int) string {
	s := m.sessions
	rows := []string{m.styles.hunk.Render("Saved sessions"), s.filter.View(), ""}
	m.clampOffset()
	for i := s.offset; i < min(s.offset+m.sessVisible(), len(s.entries)); i++ {
		e := s.entries[i]
		mark := "  "
		if i == s.cursor {
			mark = "› "
		}
		line := cellLine(mark+e.title, width)
		if i == s.cursor {
			line = m.styles.hunk.Bold(true).Reverse(true).Render(line)
		}
		rows = append(rows, line, m.styles.dim.Render(cellLine("  "+strings.TrimSpace(m.entryState(e)+"  "+nonEmpty(e.mode))+"  "+ageString(e.updated), width)), "")
	}
	if len(s.entries) == 0 {
		if s.filter.Value() != "" {
			rows = append(rows, "No sessions match this search.")
		} else {
			rows = append(rows, "No saved sessions yet.")
		}
	}
	pos := ""
	if len(s.entries) > 0 {
		pos = fmt.Sprintf("%d/%d  ", s.cursor+1, len(s.entries))
	}
	rows = append(rows, m.styles.dim.Render(pos+"Enter open"))
	if s.flash != "" {
		rows = append(rows, m.styles.err.Render(s.flash))
	}
	switch s.mode {
	case sessRename:
		rows = append(rows, "New name  Enter save  Esc back", m.note.View())
	case sessConfirm:
		if len(s.entries) > 0 {
			rows = append(rows, m.styles.err.Render("Delete "+s.entries[s.cursor].title+"? y / n"))
		}
	}
	return strings.Join(rows, "\n")
}
func (m *model) browserPreview(width int) string {
	s := m.sessions
	if len(s.entries) == 0 {
		return ""
	}
	e := s.entries[s.cursor]
	rows := []string{m.styles.hunk.Render(e.title), m.styles.dim.Render(strings.TrimSpace(m.entryState(e) + "  " + e.mode)), ""}
	if e.preview != "" {
		rows = append(rows, reflow(e.preview, max(width, 8))...)
	} else {
		rows = append(rows, "Open to inspect saved history.")
	}
	rows = append(rows, "", fmt.Sprintf("History  %d messages", e.msgs), "Saved  "+ageString(e.updated), "ID  "+e.id, "")
	switch {
	case e.mode == "Mission":
		rows = append(rows, "Resume this mission from the CLI.")
	case e.status == "Unreadable":
		rows = append(rows, "Snapshot cannot be read. Delete or repair it.")
	default:
		rows = append(rows, "Opening history does not start a run.", "Send a follow-up to continue.")
	}
	return strings.Join(rows, "\n")
}
func (m *model) browserView() string {
	width, height := max(m.termW, 0), m.vp.Height()
	if width >= 90 {
		left := min(44, (width-3)/2)
		divider := strings.Repeat(m.styles.dim.Render(" │ ")+"\n", max(height-1, 0)) + m.styles.dim.Render(" │ ")
		return lipgloss.JoinHorizontal(lipgloss.Top,
			cellFrame(m.browserList(left), left, height), divider,
			cellFrame(m.browserPreview(width-left-3), width-left-3, height))
	}
	// Small terminals keep the browser usable without squeezing the transcript.
	return cellFrame(m.browserList(width), width, height)
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
	case "enter":
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
		return m, m.openSelected()
	}
	return m, nil
}
