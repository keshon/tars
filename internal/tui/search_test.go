package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/keshon/tars/internal/api"
	"github.com/keshon/tars/internal/llm"
	"github.com/keshon/tars/internal/workspace"
)

func TestSearchIndexesConversationAndRanksTitles(t *testing.T) {
	root := filepath.Join(t.TempDir(), tasksRoot())
	title := writeSessionState(t, root, "title", userHistory("ПАРСЕР implementation"))
	history := writeSessionState(t, root, "history", []llm.Message{
		{Role: llm.RoleUser, Content: "Unrelated title"},
		{Role: llm.RoleAssistant, Content: "<think>private needle</think>\nThe парсер handles UTF-8."},
		{Role: llm.RoleUser, Content: "[harness] hidden needle"},
		{Role: llm.RoleTool, Content: "tool needle"},
		{Role: llm.RoleSystem, Content: "system needle"},
	})
	entries := listSessionsIn(root)
	s := &sessionsState{all: entries}
	s.filter = newNote()
	s.filter.SetValue("парсер")
	s.applyFilter()
	if len(s.entries) != 2 || s.entries[0].dir != title || s.entries[1].dir != history || !s.entries[1].historyMatch {
		t.Fatalf("title ranking or Unicode history match failed: %+v", s.entries)
	}
	for _, query := range []string{"private needle", "hidden needle", "tool needle", "system needle"} {
		s.filter.SetValue(query)
		s.applyFilter()
		if len(s.entries) != 0 {
			t.Fatalf("searched hidden content: %s", query)
		}
	}
	s.filter.SetValue("")
	s.applyFilter()
	if len(s.entries) != len(entries) {
		t.Fatal("empty query lost recent sessions")
	}
}

func TestSearchPopupGeometryAndDraftPreservation(t *testing.T) {
	for _, size := range [][2]int{{160, 40}, {120, 30}, {80, 24}, {40, 18}, {20, 10}} {
		m := testModel()
		m.state = stDone
		m.wsRoot = t.TempDir()
		for i := 0; i < 9; i++ {
			writeSessionState(t, filepath.Join(m.wsRoot, tasksRoot()), fmt.Sprint(i), userHistory(fmt.Sprintf("Session %d", i)))
		}
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m.input.SetValue("draft\nsecond line")
		m.fitBottom()
		m.openSessions()
		m.sessions.filter.SetValue("Session")
		m.sessions.applyFilter()
		g := m.searchGeometry()
		view := m.View()
		if strings.Contains(m.sessions.filter.View(), "…") {
			t.Fatal("search field clipped its own padding")
		}
		if view.Cursor == nil || view.Cursor.X < g.x || view.Cursor.X >= g.x+g.width || view.Cursor.Y != g.y+1 {
			t.Fatalf("size %v: caret %+v geometry %+v", size, view.Cursor, g)
		}
		for _, line := range strings.Split(view.Content, "\n") {
			if lipgloss.Width(line) != size[0] {
				t.Fatalf("size %v overflow", size)
			}
		}
		if lipgloss.Height(view.Content) != size[1] || g.x != (size[0]-g.width)/2 {
			t.Fatal("popup not centered or frame clipped")
		}
		if size[1] >= 24 && m.sessVisible() != 6 {
			t.Fatal("search did not use six result slots")
		}
		m.sessionsKey(tea.KeyPressMsg{Code: tea.KeyEsc})
		if m.input.Value() != "draft\nsecond line" {
			t.Fatal("closing search lost draft")
		}
	}
}

func TestSearchHistoryMatchOpensAtMessage(t *testing.T) {
	m := testModel()
	m.state = stDone
	m.wsRoot = t.TempDir()
	history := userHistory("Unrelated title")
	for i := 0; i < 30; i++ {
		history = append(history, llm.Message{Role: llm.RoleAssistant, Content: fmt.Sprintf("Earlier message %d", i)})
	}
	history = append(history, llm.Message{Role: llm.RoleUser, Content: "Find this unique phrase"})
	for i := 0; i < 20; i++ {
		history = append(history, llm.Message{Role: llm.RoleAssistant, Content: fmt.Sprintf("Later message %d", i)})
	}
	dir := writeSessionState(t, filepath.Join(m.wsRoot, tasksRoot()), "match", history)
	m.newSession = func(string, string, []string) (*api.Session, error) { return &api.Session{}, nil }
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m.openSessions()
	m.sessions.filter.SetValue("unique phrase")
	m.sessions.applyFilter()
	m.openSelected()
	if m.sessions != nil || m.stateFile != filepath.Join(dir, "state.json") || m.follow || m.vp.YOffset() == 0 || !strings.Contains(ansi.Strip(m.vp.View()), "Find this unique phrase") {
		t.Fatal("opening history result did not reveal matching message")
	}
}

func TestNavigatorRenameAndDeleteKeepPaneFocus(t *testing.T) {
	m := testModel()
	m.state = stDone
	m.wsRoot = t.TempDir()
	dir := writeSessionState(t, filepath.Join(m.wsRoot, tasksRoot()), "one", userHistory("Original title"))
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m.refreshNavigator()
	m.navFocused = true
	m.input.Blur()
	m.navigatorKey(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	if m.sessions == nil || m.sessions.mode != sessRename {
		t.Fatal("pane rename shortcut missing")
	}
	m.note.SetValue("Renamed")
	m.sessionsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.sessions != nil || !m.navFocused || workspace.ReadSessionTitle(dir) != "Renamed" {
		t.Fatal("rename did not return to pane")
	}
	m.navigatorKey(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if m.sessions == nil || m.sessions.mode != sessConfirm {
		t.Fatal("pane delete shortcut missing")
	}
	m.sessionsKey(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if m.sessions != nil || !m.navFocused {
		t.Fatal("cancel deletion did not return to pane")
	}
	m.navigatorKey(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	m.sessionsKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if _, err := os.Stat(dir); !os.IsNotExist(err) || m.sessions != nil || !m.navFocused {
		t.Fatal("confirmed deletion did not remove session and restore pane")
	}

}

func TestSearchHighlightExcerptKeepsUnicode(t *testing.T) {
	text := strings.Repeat("Earlier text ", 20) + "Нужный парсер работает" + strings.Repeat(" later text", 20)
	excerpt := searchExcerpt(text, "ПАРСЕР", 60)
	if !strings.Contains(excerpt, "парсер") || lipgloss.Width(excerpt) > 60 {
		t.Fatal("matching excerpt lost Unicode or overflows")
	}
	normal, _, _ := popupStyles()
	if !strings.Contains(highlightSearch(excerpt, "ПАРСЕР", normal), "38;2;243;213;138") {
		t.Fatal("matching words not highlighted")
	}
}
