package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/keshon/tars/internal/api"
)

func TestCompactSessionRowsSharePagingAndMouseGeometry(t *testing.T) {
	m := testModel()
	m.state = stDone
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m.navFocused = true
	m.input.Blur()
	for i := 0; i < 40; i++ {
		m.nav.entries = append(m.nav.entries, sessionEntry{
			title: fmt.Sprintf("Session %02d", i), mode: "Act", status: "Saved",
			updated: time.Now().Add(-2 * time.Minute), msgs: i + 1,
		})
	}
	view := ansi.Strip(m.sidebarView())
	if strings.Contains(view, "Saved") || strings.Count(view, "Session ") != m.navigatorRows() {
		t.Fatal("session rows repeated status or wasted available space")
	}
	rows := strings.Split(view, "\n")
	if !strings.Contains(ansi.Strip(m.sidebarDetails()), "Act  1 message") || strings.TrimSpace(string([]rune(rows[0])[navigatorWidth-4:])) != "2m" {
		t.Fatal("selected details or aligned age missing")
	}
	for _, label := range []string{"NAME", "AGE", "tars"} {
		if strings.Contains(view, label) {
			t.Fatalf("redundant pane label remains: %s", label)
		}
	}
	frame := strings.Split(ansi.Strip(m.View().Content), "\n")
	footer := 3 + m.paneHeight() - 1
	if strings.Contains(frame[footer-1][:navigatorWidth], "─") {
		t.Fatal("Sessions status still has its own divider")
	}
	if !strings.Contains(frame[footer], "Act  1 message") || !strings.Contains(frame[footer], "│") {
		t.Fatal("session details are not at the bottom of the pane")
	}
	rule := frame[footer+1]
	if lipgloss.Width(rule) != m.termW || strings.ReplaceAll(rule, "─", "") != "┴" {
		t.Fatal("global footer separator does not span both panes")
	}
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if m.nav.cursor != m.navigatorRows() || m.nav.offset != 1 {
		t.Fatal("paging did not use compact row capacity")
	}
	m.Update(tea.MouseClickMsg{X: 5, Y: 3, Button: tea.MouseLeft})
	if m.nav.cursor != m.nav.offset {
		t.Fatal("click missed the first visible compact row")
	}
}

func TestResponsiveNavigator(t *testing.T) {
	m := testModel()
	m.state = stDone
	m.input.SetValue("unfinished draft")
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	if !m.sidebarVisible() || m.vp.Width() != 120-navigatorWidth-3 {
		t.Fatalf("wide viewport: %d", m.vp.Width())
	}
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	if !m.navFocused || m.input.Focused() {
		t.Fatal("sidebar did not own focus")
	}
	m.handleKey(tea.KeyPressMsg{Text: string("x")})
	if m.input.Value() != "unfinished draft" {
		t.Fatal("sidebar keys edited draft")
	}
	m.handleKey(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	if m.sidebarVisible() || m.vp.Width() != 120 || !m.input.Focused() {
		t.Fatal("hidden sidebar failed to restore input")
	}
	m.handleKey(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if m.sidebarVisible() || m.vp.Width() != 80 || m.input.Value() != "unfinished draft" {
		t.Fatal("narrow resize lost draft or space")
	}
}

func TestBrowserSearchUsesWorkspace(t *testing.T) {
	m := testModel()
	m.state = stDone
	m.wsRoot = t.TempDir()
	root := filepath.Join(m.wsRoot, tasksRoot())
	writeSessionState(t, root, "aaa", userHistory("repair login"))
	writeSessionState(t, root, "bbb", userHistory("speed up search"))
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m.openSessions()
	m.sessionsKey(tea.KeyPressMsg{Text: string("repair")})
	if len(m.sessions.entries) != 1 || m.sessions.entries[0].id != "aaa" {
		t.Fatal("search did not filter workspace sessions")
	}
	if m.sessions.mode != sessList {
		t.Fatal("typing r accidentally renamed")
	}
	m.sessionsKey(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	if m.sessions.mode != sessRename {
		t.Fatal("rename shortcut failed")
	}
	m.sessionsKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	m.sessionsKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.sessions != nil {
		t.Fatal("browser did not close")
	}
}

func TestNavigatorCannotSwitchDuringRun(t *testing.T) {
	m := testModel()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m.nav.entries = []sessionEntry{{id: "aaa", title: "Saved task", dir: "aaa"}}
	m.navFocused = true
	m.stateFile = "active/state.json"
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.stateFile != "active/state.json" || m.state != stRunning || m.sessions != nil {
		t.Fatal("browse interrupted active run")
	}
	// A gate arriving during browsing takes over both rendering and keys.
	m.openSessions()
	m.handleEvent(api.Event{Name: "awaiting_input", Fields: map[string]any{"kind": "permission", "prompt": "Run tests?"}})
	if m.sessions != nil || m.navFocused || m.state != stPermission {
		t.Fatal("permission gate did not take over")
	}
}

func TestSidebarWheelSelectsWithoutScrollingChat(t *testing.T) {
	m := testModel()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m.nav.entries = []sessionEntry{{id: "a"}, {id: "b"}}
	m.Update(tea.MouseWheelMsg{X: 5, Y: 6, Button: tea.MouseWheelDown})
	if m.nav.cursor != 1 || !m.navFocused || m.input.Focused() || m.vp.YOffset() != 0 {
		t.Fatal("sidebar wheel reached the chat or failed to select")
	}
}

func TestFramesFitTerminalCells(t *testing.T) {
	for _, size := range [][2]int{{160, 40}, {110, 24}, {80, 24}, {40, 18}, {20, 10}} {
		for _, browser := range []bool{false, true} {
			m := testModel()
			m.state = stDone
			m.wsRoot = t.TempDir()
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			m.appendBlock(userBlock(strings.Repeat("Wide 界 🚀 text ", 20)))
			m.input.SetValue("first line\nsecond line")
			m.fitInput()
			m.fitBottom()
			if browser {
				m.openSessions()
			}
			lines := strings.Split(m.View().Content, "\n")
			if len(lines) != size[1] {
				t.Fatalf("size %v: %d rows", size, len(lines))
			}
			for _, line := range lines {
				if lipgloss.Width(line) > size[0] {
					t.Fatalf("size %v: overflowing line %q", size, line)
				}
			}
		}
	}
}

func TestReflowPreservesWideCharacters(t *testing.T) {
	input := strings.Repeat("界🚀é", 20)
	lines := reflow(input, 12)
	if strings.Join(lines, "") != input {
		t.Fatal("wrapping lost text or split a grapheme")
	}
	for _, line := range lines {
		if lipgloss.Width(line) > 12 {
			t.Fatalf("wide characters exceeded pane: %q", line)
		}
	}
}

func TestConversationOwnsComposerAndSessionsKeepTheirHeight(t *testing.T) {
	m := testModel()
	m.state = stDone
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m.nav.entries = []sessionEntry{{title: "Current session", mode: "Act", msgs: 2}}
	rows := m.navigatorRows()
	m.input.SetValue("first line\nsecond line")
	m.fitBottom()
	if m.navigatorRows() != rows {
		t.Fatal("multiline composer took space from Sessions")
	}
	view := m.View()
	lines := strings.Split(ansi.Strip(view.Content), "\n")
	inputTop := m.termH - m.footerRows() - m.input.Height()
	for y := 3; y < m.termH-m.footerRows(); y++ {
		if []rune(lines[y])[navigatorWidth+1] != '│' && []rune(lines[y])[navigatorWidth+1] != '┤' && []rune(lines[y])[navigatorWidth+1] != '├' {
			t.Fatalf("session divider interrupted at row %d: %q", y, lines[y])
		}
	}
	if !strings.HasPrefix(string([]rune(lines[inputTop])[m.conversationOffset():]), "> first line") {
		t.Fatal("composer did not align with the conversation")
	}
	if view.Cursor == nil || view.Cursor.X < m.conversationOffset() || view.Cursor.Y != inputTop+1 {
		t.Fatalf("caret escaped the conversation composer: %+v", view.Cursor)
	}
	m.navFocused = true
	m.input.Blur()
	m.Update(tea.MouseClickMsg{X: m.conversationOffset() + 2, Y: inputTop, Button: tea.MouseLeft})
	if m.navFocused || !m.input.Focused() {
		t.Fatal("clicking the composer did not restore input focus")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.input.Value() != "first line\nsecond line" || m.input.Height() != 2 {
		t.Fatal("composer navigation changed the multiline draft")
	}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if m.conversationOffset() != 0 || m.View().Cursor.X >= 80 || m.input.Value() != "first line\nsecond line" {
		t.Fatal("narrow layout lost the composer or its draft")
	}
}

func TestConversationComposerWrapsWithinPane(t *testing.T) {
	m := completionModel(t)
	m.Update(tea.WindowSizeMsg{Width: 110, Height: 30})
	m.Update(tea.PasteMsg{Content: strings.Repeat("text ", 20)})
	if m.input.Height() < 2 {
		t.Fatal("draft wrapped against terminal width instead of conversation width")
	}
	for _, state := range []runState{stDone, stRunning, stAsk, stPermission} {
		m.state = state
		m.fitBottom()
		view := m.View()
		if lipgloss.Height(view.Content) != m.termH {
			t.Fatalf("state %d changed frame height", state)
		}
		if view.Cursor != nil && view.Cursor.X < m.conversationOffset() {
			t.Fatalf("state %d placed caret in Sessions", state)
		}
	}
}

func TestNestedComposerCompletesFilesAndCommands(t *testing.T) {
	m := completionModel(t)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	writeReference(t, m, "notes.txt", []byte("workspace notes"))
	m.Update(tea.PasteMsg{Content: "first line"})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	m.Update(tea.PasteMsg{Content: "read @no"})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.input.Value() != "first line\nread @notes.txt " || m.input.Height() != 2 || m.state != stDone {
		t.Fatal("file completion submitted or lost the multiline draft")
	}
	view := m.View()
	if view.Cursor == nil || view.Cursor.X < m.conversationOffset() || view.Cursor.Y != m.termH-m.footerRows()-1 {
		t.Fatalf("file completion misplaced the caret: %+v", view.Cursor)
	}
	m.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	m.Update(tea.PasteMsg{Content: "/hel"})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.dialog == nil || !m.dialog.help || m.input.Value() != "" {
		t.Fatal("command completion did not open help")
	}
	m.closeDialog()
	m.state = stPermission
	m.gstage = gsReject
	m.input.Blur()
	m.note.Focus()
	m.note.SetValue("use another command")
	m.fitBottom()
	view = m.View()
	if view.Cursor == nil || view.Cursor.X < m.conversationOffset() || view.Cursor.Y != m.termH-m.footerRows()-1 {
		t.Fatalf("permission note misplaced the caret: %+v", view.Cursor)
	}
}
