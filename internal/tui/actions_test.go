package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestFooterActionsKeepDraftAndToggleScreens(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	m.input.SetValue("draft\nsecond line")
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyF1})
	if m.dialog == nil || m.dialog.title != "Help" {
		t.Fatal("help did not open")
	}
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyF1})
	if m.dialog != nil || m.input.Value() != "draft\nsecond line" {
		t.Fatal("help lost draft")
	}
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyF2})
	if m.sessions == nil {
		t.Fatal("chats did not open")
	}
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyF2})
	if m.sessions != nil || m.input.Value() != "draft\nsecond line" {
		t.Fatal("chats lost draft")
	}
	before := m.compact
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyF3})
	if m.compact == before {
		t.Fatal("details did not toggle")
	}
}

func TestFooterActionsCannotHidePendingPrompts(t *testing.T) {
	for _, state := range []runState{stPermission, stAsk} {
		m := sizeModel(t, testModel())
		m.state = state
		before := m.compact
		for _, key := range []rune{tea.KeyF1, tea.KeyF2, tea.KeyF3, tea.KeyF4, tea.KeyF5, tea.KeyF6, tea.KeyF7} {
			m.handleKey(tea.KeyPressMsg{Code: key})
		}
		if m.dialog != nil || m.sessions != nil || m.state != state || m.compact != before {
			t.Fatal("function key hid prompt")
		}
		m.handleKey(tea.KeyPressMsg{Code: tea.KeyF10})
		if m.quit || m.state != stStopping {
			t.Fatal("quit must first stop the active run")
		}
	}
}

func TestActionBarCellsAndMouseShareGeometry(t *testing.T) {
	for _, width := range []int{40, 80, 120} {
		m := sizeModel(t, testModel())
		m.state = stDone
		m.width(tea.WindowSizeMsg{Width: width, Height: 24})
		rows := strings.Split(ansi.Strip(m.View().Content), "\n")
		top := len(rows) - m.actionBarRows()
		for _, cell := range m.footerCells() {
			bar := rows[top+cell.row]
			if !strings.HasPrefix(string([]rune(bar)[cell.start:]), func() string {
				if cell.end-cell.start < 14 && strings.HasPrefix(cell.action.name, "Ctrl+") {
					return "^" + strings.TrimPrefix(cell.action.name, "Ctrl+")
				}
				return cell.action.name
			}()) {
				t.Fatalf("button moved at width %d: %q", width, bar)
			}
			if cell.start > 0 && []rune(bar)[cell.start-1] != ' ' {
				t.Fatal("button separator missing")
			}
			if cell.action.key == 'p' {
				m.Update(tea.MouseClickMsg{X: cell.start, Y: top + cell.row, Button: tea.MouseLeft})
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
	if strings.Contains(m.statusLine(), "step") || !strings.Contains(m.View().Content, "step 7/25") {
		t.Fatal("metrics mixed with key hints")
	}
	rows := strings.Split(ansi.Strip(m.View().Content), "\n")
	if !strings.Contains(rows[1], "step 7/25") || strings.Count(strings.Join(rows, "\n"), "step 7/25") != 1 {
		t.Fatal("run metrics must appear once in the top status row")
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "▸ CHAT") {
		t.Fatal("input focus not identified")
	}
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	if !strings.Contains(ansi.Strip(m.View().Content), "▸ SESSIONS") || !m.navFocused {
		t.Fatal("chat pane focus not identified")
	}
	m.Update(tea.MouseClickMsg{X: 60, Y: 5, Button: tea.MouseLeft})
	if m.navFocused || !m.input.Focused() {
		t.Fatal("click did not return focus to input")
	}
}

func TestHeaderPrioritizesRunMetricsOnNarrowTerminals(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stRunning
	m.steps, m.maxSteps = 2, 25
	m.tokens, m.limit = 4100, 16400
	m.elapsed = 32 * time.Second
	m.toolsUsed = 1
	m.modelName = "Qwen3.5-9B-Q4_K_M"
	for _, width := range []int{80, 120} {
		m.width(tea.WindowSizeMsg{Width: width, Height: 30})
		line := ansi.Strip(m.identityLine())
		for _, fact := range []string{"step 2/25", "CTX 4.1k / 16.4k", "00:32"} {
			if !strings.Contains(line, fact) {
				t.Fatalf("header at width %d lost %q: %q", width, fact, line)
			}
		}
		if strings.Contains(line, "context ") {
			t.Fatal("context limit duplicated beside run metrics")
		}
	}
}

func TestHelpKeepsFooterOnLastRowAndHidesComposer(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	m.input.SetValue("first\nsecond\nthird")
	m.fitInput()
	m.fitBottom()
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyF1})
	rows := strings.Split(ansi.Strip(m.View().Content), "\n")
	if !strings.Contains(rows[len(rows)-1], "Ctrl+Q Quit") || strings.Contains(ansi.Strip(m.View().Content), "first") || !strings.Contains(m.View().Content, "Draft preserved") {
		t.Fatal("help moved footer or exposed active composer")
	}
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyF1})
	if m.input.Value() != "first\nsecond\nthird" || m.input.Height() != 3 || !m.input.Focused() {
		t.Fatal("help did not restore composer")
	}
}

func TestFooterModeAndNewPreserveDraft(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	m.input.SetValue("unsent task")
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyF5})
	if !m.plan || m.input.Value() != "unsent task" {
		t.Fatal("mode change lost draft")
	}
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyF5})
	if m.plan {
		t.Fatal("mode did not toggle back")
	}
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyF4})
	if m.input.Value() != "unsent task" {
		t.Fatal("new on unsaved chat lost draft")
	}
}

func TestSidebarHasOneHeadingAndNoShortcutCluster(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	m.width(tea.WindowSizeMsg{Width: 120, Height: 30})
	view := ansi.Strip(m.View().Content)
	if strings.Count(view, "SESSIONS") != 1 || strings.Contains(m.sidebarView(), "Ctrl+") || strings.Contains(m.identityLine(), "Chat input") {
		t.Fatal("duplicate chat heading or shortcut clutter")
	}
}

func TestToolbarContextAndQuitProtection(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	m.follow = true
	actions := m.footerActions()
	if actions[0].label != "Help" || actions[1].label != "Search" || actions[2].label != "New" || actions[3].label != "Send" || actions[4].label != "Newline" {
		t.Fatal("composer actions")
	}
	m.input.SetValue("draft")
	m.handleKey(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if !strings.Contains(m.input.Value(), "\n") {
		t.Fatal("toolbar newline shortcut")
	}
	m.navFocused = true
	if m.footerActions()[3].label != "Rename" {
		t.Fatal("navigator actions")
	}
	m.navFocused = false
	m.state = stRunning
	if m.footerActions()[3].label != "Stop" {
		t.Fatal("run actions")
	}
	m.handleKey(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl})
	if m.quit || m.state != stStopping {
		t.Fatal("active run quit without stopping")
	}
	m.handleKey(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl})
	if m.quit {
		t.Fatal("quit before stop completed")
	}
	m.state = stDone
	m.handleKey(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl})
	if !m.quit {
		t.Fatal("idle quit")
	}
}

func TestSearchToolbarShortcutClosesPopup(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	key := tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl}
	m.handleKey(key)
	if m.sessions == nil {
		t.Fatal("search shortcut did not open")
	}
	m.handleKey(key)
	if m.sessions != nil {
		t.Fatal("search shortcut did not close")
	}
}

func TestDetailsToolbarRemainsVisibleAndClickable(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	m.width(tea.WindowSizeMsg{Width: 160, Height: 30})
	for _, compact := range []bool{true, false} {
		m.compact = compact
		label := "Details"
		if !compact {
			label = "Less"
		}
		found := false
		for _, cell := range m.footerCells() {
			if cell.action.key != 'g' {
				continue
			}
			found = true
			if cell.action.label != label || !cell.action.enabled {
				t.Fatal("incorrect details action")
			}
			m.Update(tea.MouseClickMsg{X: cell.start, Y: m.termH - m.actionBarRows() + cell.row, Button: tea.MouseLeft})
			if m.compact == compact {
				t.Fatal("details click did not toggle")
			}
		}
		if !found {
			t.Fatal("details action missing")
		}
	}
}
