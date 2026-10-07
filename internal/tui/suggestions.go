package tui

import (
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type suggestion struct {
	value, description string
	directory          bool
}

type suggestionState struct {
	items                []suggestion
	selected, start, end int
	key, dismissed       string
}

// Positions are rune offsets, matching textarea's logical row and column.
func (m *model) completionToken() (start, end int, prefix string) {
	runes := []rune(m.input.Value())
	pos := m.input.Column()
	for _, line := range strings.Split(m.input.Value(), "\n")[:m.input.Line()] {
		pos += len([]rune(line)) + 1
	}
	pos = min(pos, len(runes))
	quoted := false
	for i, r := range runes[:pos] {
		if r == '"' {
			quoted = !quoted
		} else if unicode.IsSpace(r) && !quoted {
			start = i + 1
		}
	}
	before := string(runes[:pos])
	if strings.HasPrefix(strings.ToLower(strings.TrimLeftFunc(before, unicode.IsSpace)), "/mode ") {
		start = len([]rune(before)) - len([]rune(strings.TrimLeftFunc(before, unicode.IsSpace)))
	}
	prefix = string(runes[start:pos])
	if prefix == "" || (prefix[0] != '@' && (prefix[0] != '/' || strings.TrimSpace(string(runes[:start])) != "")) {
		return 0, 0, ""
	}
	end = pos
	for end < len(runes) {
		if unicode.IsSpace(runes[end]) && !quoted {
			break
		}
		if runes[end] == '"' {
			quoted = !quoted
		}
		end++
	}
	return start, end, prefix
}

func (m *model) syncSuggestions() {
	s := &m.suggestions
	if m.headerFocused || m.navFocused || m.dialog != nil || m.sessions != nil || (m.ready && (m.vp.Height() < 4 || m.vp.Width() < 24)) || (m.state != stDone && m.state != stRunning) {
		s.items = nil
		s.key = ""
		return
	}
	start, end, prefix := m.completionToken()
	key := m.input.Value() + "\x00" + strconv.Itoa(start) + ":" + strconv.Itoa(end) + ":" + prefix
	if prefix == "" {
		s.items, s.key, s.dismissed = nil, "", ""
		return
	}
	if key == s.key {
		return
	}
	s.key, s.start, s.end, s.selected = key, start, end, 0
	s.items = nil
	if key == s.dismissed {
		return
	}
	if prefix[0] == '/' {
		for _, row := range commandReference() {
			name, _, _ := strings.Cut(row[0], " ")
			names := []string{name}
			if name == "/mode" && strings.Contains(prefix, " ") {
				names = []string{"/mode plan", "/mode act"}
			}
			for _, candidate := range names {
				if strings.HasPrefix(candidate, strings.ToLower(prefix)) {
					s.items = append(s.items, suggestion{value: candidate, description: row[1]})
				}
			}
		}
		return
	}
	if m.ws == nil {
		return
	}
	path := strings.TrimPrefix(strings.TrimPrefix(prefix, "@"), "\"")
	path = strings.ReplaceAll(path, "\\", "/")
	dir, name := "", path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		dir, name = path[:i+1], path[i+1:]
	}
	full, err := referencePath(m.ws, dir)
	if err != nil {
		return
	}
	f, err := os.Open(full)
	if err != nil {
		return
	}
	// Bound work on each edit; browse one directory rather than walking the repo.
	entries, err := f.ReadDir(2048)
	f.Close()
	if err != nil && err != io.EOF {
		return
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
	})
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || (!strings.HasPrefix(name, ".") && strings.HasPrefix(entry.Name(), ".")) || !strings.HasPrefix(strings.ToLower(entry.Name()), strings.ToLower(name)) {
			continue
		}
		value := filepath.ToSlash(dir + entry.Name())
		if !entry.IsDir() && path == value {
			continue
		}
		description := "File reference"
		if entry.IsDir() {
			value += "/"
			description = "Browse directory"
		}
		s.items = append(s.items, suggestion{value, description, entry.IsDir()})
		if len(s.items) == 64 {
			break
		}
	}
}

func (m *model) suggestionKey(msg tea.KeyPressMsg) bool {
	s := &m.suggestions
	if len(s.items) == 0 {
		return false
	}
	switch msg.String() {
	case "up":
		s.selected = (s.selected + len(s.items) - 1) % len(s.items)
	case "down":
		s.selected = (s.selected + 1) % len(s.items)
	case "esc":
		s.dismissed, s.items = s.key, nil
	case "tab", "enter":
		item := s.items[s.selected]
		value := item.value
		runes := []rune(m.input.Value())
		if runes[s.start] == '@' {
			if strings.ContainsAny(value, " \t") {
				value = "@\"" + value + "\""
			} else {
				value = "@" + value
			}
		}
		if !item.directory {
			value += " "
		}
		before := string(runes[:s.start]) + value
		after := string(runes[s.end:])
		if !item.directory {
			after = strings.TrimPrefix(after, " ")
		}
		m.input.SetValue(before + after)
		m.input.MoveToBegin()
		line := strings.Count(before, "\n")
		for m.input.Line() < line {
			m.input.CursorDown()
		}
		column := len([]rune(before[strings.LastIndex(before, "\n")+1:]))
		if item.directory && strings.HasSuffix(value, "\"") {
			column-- // Keep typing inside the quoted directory path.
		}
		m.input.SetCursorColumn(column)
		s.key = ""
		m.fitInput()
		m.fitBottom()
	default:
		return false
	}
	return true
}

// Replace the bottom of the transcript; the composer and its caret stay put.
func (m *model) suggestionView(body string) string {
	s := &m.suggestions
	if len(s.items) == 0 || m.vp.Height() < 4 {
		return body
	}
	width := min(m.vp.Width(), 78)
	if width < 24 {
		return body
	}
	height := max(m.vp.Height(), lipgloss.Height(body))
	count := min(6, height-3, len(s.items))
	start := min(max(s.selected-count+1, 0), len(s.items)-count)
	normal, border, selected := popupStyles()
	title, action := "Files", "Enter / Tab insert"
	if strings.HasPrefix(s.items[0].value, "/") {
		title, action = "Commands", "Enter run  Tab insert"
	}
	rows := []string{border.Render("╭─ " + title + " " + strings.Repeat("─", width-len(title)-5) + "╮")}
	for i := start; i < start+count; i++ {
		item := s.items[i]
		mark, style := "  ", normal
		if i == s.selected {
			mark, style = "› ", selected
		}
		label := mark + item.value + "  " + item.description
		rows = append(rows, border.Render("│")+style.Render(cellLine(label, width-2))+border.Render("│"))
	}
	hint := "↑↓ select  " + action + "  Esc dismiss"
	rows = append(rows, border.Render("│")+normal.Render(cellLine("  "+hint, width-2))+border.Render("│"))
	rows = append(rows, border.Render("╰"+strings.Repeat("─", width-2)+"╯"))

	lines := strings.Split(cellFrame(body, m.vp.Width(), height), "\n")
	return strings.Join(append(lines[:len(lines)-len(rows)], rows...), "\n")
}
