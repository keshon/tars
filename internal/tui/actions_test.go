package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestFooterActionsKeepDraftAndToggleScreens(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	m.input.SetValue("draft\nsecond line")
	m.handleKey(tea.KeyMsg{Type: tea.KeyF1})
	if m.dialog == nil || m.dialog.title != "Help" {
		t.Fatal("help did not open")
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyF1})
	if m.dialog != nil || m.input.Value() != "draft\nsecond line" {
		t.Fatal("help lost draft")
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyF2})
	if m.sessions == nil {
		t.Fatal("chats did not open")
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyF2})
	if m.sessions != nil || m.input.Value() != "draft\nsecond line" {
		t.Fatal("chats lost draft")
	}
	before := m.compact
	m.handleKey(tea.KeyMsg{Type: tea.KeyF3})
	if m.compact == before {
		t.Fatal("details did not toggle")
	}
}

func TestFooterActionsCannotHidePendingPrompts(t *testing.T) {
	for _, state := range []runState{stPermission, stAsk} {
		m := sizeModel(t, testModel())
		m.state = state
		before := m.compact
		for _, key := range []tea.KeyType{tea.KeyF1, tea.KeyF2, tea.KeyF3, tea.KeyF4, tea.KeyF5, tea.KeyF6, tea.KeyF7} {
			m.handleKey(tea.KeyMsg{Type: key})
		}
		if m.dialog != nil || m.sessions != nil || m.state != state || m.compact != before {
			t.Fatal("function key hid prompt")
		}
		m.handleKey(tea.KeyMsg{Type: tea.KeyF10})
		if !m.quit {
			t.Fatal("quit blocked on prompt")
		}
	}
}

func TestActionBarCellsAndMouseShareGeometry(t *testing.T) {
	for _, width := range []int{40, 80, 120} {
		m := sizeModel(t, testModel())
		m.state = stDone
		m.width(tea.WindowSizeMsg{Width: width, Height: 24})
		rows := strings.Split(ansi.Strip(m.View()), "\n")
		top := len(rows) - m.actionBarRows()
		for _, cell := range m.footerCells() {
			bar := rows[top+cell.row]
			if !strings.HasPrefix(string([]rune(bar)[cell.start:]), strings.TrimPrefix(cell.action.name, "F")) {
				t.Fatalf("button moved at width %d: %q", width, bar)
			}
			if cell.start > 0 && []rune(bar)[cell.start-1] != ' ' {
				t.Fatal("button separator missing")
			}
			if cell.action.key == tea.KeyF2 {
				m.Update(tea.MouseMsg{X: cell.start, Y: top + cell.row, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
				if m.sessions == nil {
					t.Fatalf("chat button click missed at width %d", width)
				}
			}
		}
	}
}

func TestFocusAndMetricsHaveDedicatedPlaces(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	m.width(tea.WindowSizeMsg{Width: 120, Height: 30})
	m.steps, m.maxSteps = 7, 25
	if strings.Contains(m.statusLine(), "step") || !strings.Contains(m.View(), "step 7/25") {
		t.Fatal("metrics mixed with key hints")
	}
	if !strings.Contains(ansi.Strip(m.View()), "▸ CHAT") {
		t.Fatal("input focus not identified")
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	if !strings.Contains(ansi.Strip(m.View()), "▸ SESSIONS") || !m.navFocused {
		t.Fatal("chat pane focus not identified")
	}
	m.Update(tea.MouseMsg{X: 60, Y: 5, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if m.navFocused || !m.input.Focused() {
		t.Fatal("click did not return focus to input")
	}
}

func TestHelpKeepsFooterOnLastRowAndHidesComposer(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	m.input.SetValue("first\nsecond\nthird")
	m.fitInput()
	m.fitBottom()
	m.handleKey(tea.KeyMsg{Type: tea.KeyF1})
	rows := strings.Split(ansi.Strip(m.View()), "\n")
	if !strings.Contains(rows[len(rows)-1], "10Quit") || strings.Contains(m.View(), "Task>") || !strings.Contains(m.View(), "Draft preserved") {
		t.Fatal("help moved footer or exposed active composer")
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyF1})
	if m.input.Value() != "first\nsecond\nthird" || m.input.Height() != 3 || !m.input.Focused() {
		t.Fatal("help did not restore composer")
	}
}

func TestFooterModeAndNewPreserveDraft(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	m.input.SetValue("unsent task")
	m.handleKey(tea.KeyMsg{Type: tea.KeyF5})
	if !m.plan || m.input.Value() != "unsent task" {
		t.Fatal("mode change lost draft")
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyF5})
	if m.plan {
		t.Fatal("mode did not toggle back")
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyF4})
	if m.input.Value() != "unsent task" {
		t.Fatal("new on unsaved chat lost draft")
	}
}

func TestSidebarHasOneHeadingAndNoShortcutCluster(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	m.width(tea.WindowSizeMsg{Width: 120, Height: 30})
	view := ansi.Strip(m.View())
	if strings.Count(view, "SESSIONS") != 1 || strings.Contains(m.sidebarView(), "Ctrl+") || strings.Contains(m.identityLine(), "Chat input") {
		t.Fatal("duplicate chat heading or shortcut clutter")
	}
}
