package tui

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type sessionText struct {
	text  string
	block int
}
type searchGeometry struct{ x, y, width, height, noteRow int }

// Search geometry owns render, caret and hit testing in terminal cells.
func (m *model) searchGeometry() searchGeometry {
	width := max(8, min(78, m.termW-4))
	count := min(m.sessVisible(), max(len(m.sessions.entries), 1))
	height := m.sessChrome() + 2*count
	top := 3 + max((m.paneHeight()-height)/2, 0)
	noteRow := top + 3 + 2*count + 1
	if m.sessions.flash != "" {
		noteRow++
	}
	return searchGeometry{max((m.termW-width)/2, 0), top, width, height, noteRow}
}

func (m *model) searchView() string {
	state := *m.sessions
	state.clamp(m.sessVisible())
	s := &state
	g := m.searchGeometry()
	normal, border, selected := popupStyles()
	inner := g.width - 2
	query := strings.TrimSpace(s.filter.Value())
	rows := []string{border.Render("╭─ Search " + strings.Repeat("─", max(g.width-11, 0)) + "╮")}
	line := func(text string, style lipgloss.Style) string {
		return border.Render("│") + style.Render(cellLine(text, inner)) + border.Render("│")
	}
	rows = append(rows, line(s.filter.View(), normal), line("", normal))
	if len(s.entries) == 0 {
		label := "No saved sessions"
		if query != "" {
			label = "No matching sessions"
		}
		if s.loading {
			label = "Loading history…"
		}
		rows = append(rows, line(label, normal), line("", normal))
	}
	for i := s.offset; i < min(s.offset+m.sessVisible(), len(s.entries)); i++ {
		e := s.entries[i]
		mark, style := "  ", normal
		if i == s.cursor {
			mark, style = "› ", selected
		}
		age := strings.TrimSuffix(ageString(e.updated), " ago")
		if age == "just now" {
			age = "now"
		}
		ageWidth := min(lipgloss.Width(age), max(inner/3, 1))
		title := cellLine(mark+e.title, max(inner-ageWidth-1, 1))
		ageStyle := style.Foreground(lipgloss.Color(colorSecondaryText))
		rows = append(rows, border.Render("│")+highlightSearch(title, query, style)+style.Render(" ")+ageStyle.Render(cellLine(age, ageWidth))+border.Render("│"))
		text := e.preview
		if e.matchText != "" {
			text = e.matchText
		}
		if text == "" {
			text = e.mode
		}
		snippet := searchExcerpt(text, query, max(inner-2, 1))
		rows = append(rows, border.Render("│")+highlightSearch(cellLine("  "+snippet, inner), query, style.Foreground(lipgloss.Color(colorSecondaryText)))+border.Render("│"))
	}
	if s.flash != "" {
		rows = append(rows, line(s.flash, normal.Foreground(lipgloss.Color(colorPopupError))))
	}
	switch s.mode {
	case sessRename:
		rows = append(rows, line("Rename session · Enter save · Esc back", normal), line(m.note.View(), normal))
	case sessConfirm:
		title := "session"
		if len(s.entries) > 0 {
			title = s.entries[s.cursor].title
		}
		rows = append(rows, line("Delete "+title+"? Y delete · N cancel", normal))
	}
	hint := "↑↓ select · Enter open · Esc close"
	if len(s.entries) > 0 {
		hint += " · " + strconv.Itoa(s.cursor+1) + "/" + strconv.Itoa(len(s.entries))
	}
	rows = append(rows, line(hint, normal), border.Render("╰"+strings.Repeat("─", inner)+"╯"))
	return strings.Join(rows, "\n")
}

func searchExcerpt(text, query string, width int) string {
	text = strings.Join(strings.Fields(cleanText(text)), " ")
	runes := []rune(text)
	start := 0
	if query != "" {
		if loc := regexp.MustCompile("(?i)" + regexp.QuoteMeta(query)).FindStringIndex(text); loc != nil {
			start = max(utf8.RuneCountInString(text[:loc[0]])-width/4, 0)
		}
	}
	if start > 0 {
		text = "…" + string(runes[start:])
	}
	return strings.TrimRight(cellLine(text, width), " ")
}

func highlightSearch(text, query string, style lipgloss.Style) string {
	if query == "" {
		return style.Render(text)
	}
	matches := regexp.MustCompile("(?i)"+regexp.QuoteMeta(query)).FindAllStringIndex(text, -1)
	var out strings.Builder
	pos := 0
	for _, loc := range matches {
		out.WriteString(style.Render(text[pos:loc[0]]))
		out.WriteString(style.Foreground(lipgloss.Color(colorSearchMatch)).Bold(true).Render(text[loc[0]:loc[1]]))
		pos = loc[1]
	}
	out.WriteString(style.Render(text[pos:]))
	return out.String()
}

func (m *model) overlaySearch(background string) string {
	g := m.searchGeometry()
	lines := strings.Split(background, "\n")
	for i, row := range strings.Split(m.searchView(), "\n") {
		y := g.y + i
		if y >= len(lines) {
			break
		}
		lines[y] = ansi.Cut(lines[y], 0, g.x) + row + ansi.Cut(lines[y], g.x+g.width, m.termW)
	}
	return strings.Join(lines, "\n")
}
