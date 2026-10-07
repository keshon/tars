package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/keshon/tars/internal/api"
	"github.com/keshon/tars/internal/llm"
)

func TestHeaderMenusPreserveDraftAndFrame(t *testing.T) {
	for _, width := range []int{40, 80, 120} {
		m := sizeModel(t, testModel())
		m.state = stDone
		m.width(tea.WindowSizeMsg{Width: width, Height: 24})
		m.input.SetValue("draft\nsecond line")
		m.fitInput()
		m.fitBottom()
		m.handleKey(tea.KeyPressMsg{Code: tea.KeyF9})
		m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
		if m.header.menu != "mode" {
			t.Fatal("mode menu not opened")
		}
		for _, row := range strings.Split(m.View().Content, "\n") {
			if lipgloss.Width(row) != width {
				t.Fatalf("frame width %d: %q", width, ansi.Strip(row))
			}
		}
		m.handleKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
		m.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
		m.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
		if m.header.focused || m.input.Value() != "draft\nsecond line" {
			t.Fatal("header altered draft or retained focus")
		}
	}
}

func TestHeaderMouseActionsAndGateIsolation(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	m.Update(tea.MouseClickMsg{X: 1, Y: 1, Button: tea.MouseLeft})
	if m.header.menu != "mode" {
		t.Fatal("badge click missed")
	}
	m.Update(tea.MouseClickMsg{X: 2, Y: 4, Button: tea.MouseLeft})
	if !m.plan || m.header.focused {
		t.Fatal("Plan action did not execute")
	}
	m.state = stPermission
	m.Update(tea.MouseClickMsg{X: 1, Y: 1, Button: tea.MouseLeft})
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyF9})
	if m.header.focused {
		t.Fatal("header hid permission prompt")
	}
}

func TestHeaderContextPersistsAndRestoresEstimate(t *testing.T) {
	m := sizeModel(t, testModel())
	m.modelName = "test-model"
	m.stateFile = filepath.Join(t.TempDir(), "state.json")
	m.handleEvent(api.Event{Name: "usage", Fields: map[string]any{"prompt": float64(4100)}})
	if m.tokens != 4100 || !m.usage.thisRun {
		t.Fatal("usage not captured")
	}
	m.persistUsage()
	if _, err := os.Stat(filepath.Join(filepath.Dir(m.stateFile), "usage.json")); err != nil {
		t.Fatal(err)
	}
	m.restoreContext([]llm.Message{{Role: llm.RoleUser, Content: "hello", Reasoning: strings.Repeat("ignored", 1000)}})
	if !m.tokensEst || m.tokens != 6 || m.usage.last.Tokens != 4100 || m.usage.source != "Saved history estimate" {
		t.Fatalf("restored context: %d %+v", m.tokens, m.usage.last)
	}
	m.clearContext()
	if m.tokens != 0 || !m.usage.at.IsZero() || m.usage.last.Tokens != 0 {
		t.Fatal("new session retained context")
	}
}

func TestHeaderEndpointSanitizationAndLongDetails(t *testing.T) {
	got := safeEndpoint("https://user:secret@example.com/v1?api_key=secret#secret")
	if got != "https://example.com/v1" {
		t.Fatalf("unsafe endpoint: %q", got)
	}
	m := sizeModel(t, testModel())
	m.width(tea.WindowSizeMsg{Width: 40, Height: 14})
	m.header.menu = "model"
	m.modelName = strings.Repeat("long-model-", 10)
	m.header.focused = true
	items := m.headerItems()
	var joined string
	for _, item := range items {
		joined += item.text
	}
	if !strings.Contains(joined, m.modelName) {
		t.Fatal("long model lost from details")
	}
	m.header.row = len(items) - 1
	rows := strings.Split(m.overlayHeader(m.View().Content), "\n")
	if len(rows) != 14 {
		t.Fatal("menu changed frame height")
	}
}

func TestHeaderStopWorksFromSessionPaneAndGateClosesMenu(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stRunning
	m.navFocused = true
	cancelled := false
	m.cancel = func() { cancelled = true }
	m.header.focused = true
	m.header.menu = "activity"
	m.header.row = len(m.headerItems()) - 1
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !cancelled || m.state != stStopping || !m.interrupted {
		t.Fatal("Stop action was routed to the session pane")
	}
	m.header.focused = true
	m.header.menu = "model"
	m.handleEvent(api.Event{Name: "awaiting_input", Fields: map[string]any{"kind": "ask", "prompt": "Confirm?"}})
	if m.header.focused || m.header.menu != "" {
		t.Fatal("menu obscured new gate")
	}
}

func TestHeaderDetailColumns(t *testing.T) {
	m := sizeModel(t, testModel())
	for _, menu := range []string{"activity", "model", "context", "run"} {
		m.header.menu = menu
		column := -1
		for _, item := range m.headerItems() {
			colon := strings.Index(item.text, ":")
			if colon < 0 || item.command != "" {
				continue
			}
			value := colon + 1
			for value < len(item.text) && item.text[value] == ' ' {
				value++
			}
			if column < 0 {
				column = value
			}
			if value != column {
				t.Fatalf("%s: uneven value column: %q", menu, item.text)
			}
		}
	}
	m.header.menu = "context"
	m.tokens, m.limit, m.tokensEst = 100, 1000, true
	if got := m.headerItems()[0].text; !strings.HasPrefix(got, "Context:") || strings.Contains(got, "ctx") {
		t.Fatalf("context label: %q", got)
	}
}
