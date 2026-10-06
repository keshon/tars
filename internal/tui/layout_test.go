package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/keshon/tars/internal/api"
)

func TestResponsiveNavigator(t *testing.T) {
	m := testModel()
	m.state = stDone
	m.input.SetValue("unfinished draft")
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	if !m.sidebarVisible() || m.vp.Width != 88 {
		t.Fatalf("wide viewport: %d", m.vp.Width)
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	if !m.navFocused || m.input.Focused() {
		t.Fatal("sidebar did not own focus")
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if m.input.Value() != "unfinished draft" {
		t.Fatal("sidebar keys edited draft")
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlB})
	if m.sidebarVisible() || m.vp.Width != 119 || !m.input.Focused() {
		t.Fatal("hidden sidebar failed to restore input")
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlB})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if m.sidebarVisible() || m.vp.Width != 79 || m.input.Value() != "unfinished draft" {
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
	m.sessionsKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("repair")})
	if len(m.sessions.entries) != 1 || m.sessions.entries[0].id != "aaa" {
		t.Fatal("search did not filter workspace sessions")
	}
	if m.sessions.mode != sessList {
		t.Fatal("typing r accidentally renamed")
	}
	m.sessionsKey(tea.KeyMsg{Type: tea.KeyCtrlR})
	if m.sessions.mode != sessRename {
		t.Fatal("rename shortcut failed")
	}
	m.sessionsKey(tea.KeyMsg{Type: tea.KeyEsc})
	m.sessionsKey(tea.KeyMsg{Type: tea.KeyEsc})
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
	m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
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
	m.Update(tea.MouseMsg{X: 5, Y: 6, Button: tea.MouseButtonWheelDown})
	if m.nav.cursor != 1 || !m.navFocused || m.input.Focused() || m.vp.YOffset != 0 {
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
			lines := strings.Split(m.View(), "\n")
			if len(lines) != size[1]-1 {
				t.Fatalf("size %v: %d rows", size, len(lines))
			}
			for _, line := range lines {
				if lipgloss.Width(line) >= size[0] {
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
