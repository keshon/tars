package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/keshon/tars/internal/api"
	"github.com/keshon/tars/internal/llm"
	"github.com/keshon/tars/internal/workspace"
)

func TestPermissionDecisionAlwaysHasVisibleContext(t *testing.T) {
	m := sizeModel(t, testModel())
	for i := 0; i < 30; i++ {
		m.appendBlock(userBlock(fmt.Sprintf("History %d", i)))
	}
	m.vp.GotoTop()
	m.follow = false
	m.handleEvent(api.Event{Name: "awaiting_input", Fields: map[string]any{"kind": "permission", "prompt": "TARGET_SCOPE\n" + strings.Repeat("preview line\n", 40)}})
	if !strings.Contains(m.View(), "TARGET_SCOPE") {
		t.Fatal("permission context offscreen")
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyPgDown})
	if m.gateVP.YOffset == 0 {
		t.Fatal("permission preview cannot scroll")
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if !strings.Contains(m.View(), "note>") {
		t.Fatal("rejection input clipped")
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.gstage != gsPermit || strings.Contains(m.View(), "note>") {
		t.Fatal("rejection did not close")
	}
	if len(strings.Split(m.View(), "\n")) > m.termH {
		t.Fatal("gate overflowed terminal")
	}
}

func TestChatSwitchPreservesDraftAndResetsRunFacts(t *testing.T) {
	root := t.TempDir()
	a := writeSessionState(t, root, "a", userHistory("First"))
	b := writeSessionState(t, root, "b", userHistory("Second"))
	m := sessionsTestModel(t, listSessionsIn(root))
	m.stateFile = filepath.Join(a, "state.json")
	m.input.SetValue("First draft\nsecond line")
	m.toolsUsed = 7
	for i, e := range m.sessions.entries {
		if e.dir == b {
			m.sessions.cursor = i
		}
	}
	m.openSelected()
	if m.toolsUsed != 0 || m.input.Value() != "" {
		t.Fatal("stale state in second chat")
	}
	m.input.SetValue("Second draft")
	m.sessions = &sessionsState{entries: listSessionsIn(root)}
	for i, e := range m.sessions.entries {
		if e.dir == a {
			m.sessions.cursor = i
		}
	}
	m.openSelected()
	if m.input.Value() != "First draft\nsecond line" || m.input.Height() != 2 {
		t.Fatal("draft not restored")
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.input.Value() == "" {
		t.Fatal("Escape discarded draft")
	}
}

func TestQueueCanBeEditedCancelledAndSurvivesFailure(t *testing.T) {
	m := sizeModel(t, testModel())
	m.queued = "Queued task"
	m.fitBottom()
	if !strings.Contains(m.View(), "Ctrl+X cancel") {
		t.Fatal("queue controls missing")
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlE})
	if m.input.Value() != "Queued task" || m.queued != "" {
		t.Fatal("queue not editable")
	}
	m.queued = "Another task"
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlX})
	if m.queued != "" || m.input.Value() != "Queued task" {
		t.Fatal("queue cancel changed draft")
	}
	m.queued = "Keep me"
	m.Update(doneMsg{err: errors.New("backend down")})
	if m.queued != "Keep me" || m.input.Value() != "Queued task" {
		t.Fatal("failure overwrote queued work or draft")
	}
}

func TestGlobalQuitWorksInEveryPermissionStage(t *testing.T) {
	for _, stage := range []gateStage{gsPermit, gsAlways, gsReject} {
		m := sizeModel(t, testModel())
		m.state = stPermission
		m.gstage = stage
		m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
		if !m.quit {
			t.Fatalf("quit ignored in stage %d", stage)
		}
	}
}

func TestSessionBrowserMouseSelects(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	m.sessions = &sessionsState{entries: []sessionEntry{{id: "a"}, {id: "b"}}}
	m.Update(tea.MouseMsg{X: 5, Y: 9, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if m.sessions.cursor != 1 {
		t.Fatal("click ignored")
	}
}

func TestEmptyFoldersHiddenAndUnreadableMarked(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "empty", "snapshots"), 0755)
	os.MkdirAll(filepath.Join(root, "broken"), 0755)
	os.WriteFile(filepath.Join(root, "broken", "state.json"), []byte("broken"), 0644)
	entries := listSessionsIn(root)
	if len(entries) != 1 || entries[0].status != "Unreadable" {
		t.Fatalf("entries: %+v", entries)
	}
}

func TestJumpToLatestWorksWhenIdle(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	for i := 0; i < 30; i++ {
		m.appendBlock(userBlock(fmt.Sprintf("Message %d", i)))
	}
	m.vp.GotoTop()
	m.follow = false
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlEnd})
	if !m.follow || !m.vp.AtBottom() {
		t.Fatal("jump ignored")
	}
}

func TestModePersistsAcrossReopening(t *testing.T) {
	root := t.TempDir()
	dir := writeSessionState(t, root, "plan", userHistory("Plan changes"))
	if err := writeChatMode(filepath.Join(dir, "state.json"), true); err != nil {
		t.Fatal(err)
	}
	m := sessionsTestModel(t, listSessionsIn(root))
	m.openSelected()
	if !m.plan || m.modeName() != "Plan" {
		t.Fatal("saved plan mode lost")
	}
}

func TestQuestionHasScrollablePreviewAndPreservesDraftOnStop(t *testing.T) {
	m := sizeModel(t, testModel())
	m.input.SetValue("Original draft")
	m.handleEvent(api.Event{Name: "awaiting_input", Fields: map[string]any{"kind": "question", "prompt": "QUESTION_SCOPE\n" + strings.Repeat("question detail\n", 40)}})
	if !strings.Contains(m.View(), "QUESTION_SCOPE") {
		t.Fatal("question is hidden")
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyPgDown})
	if m.gateVP.YOffset == 0 {
		t.Fatal("question cannot scroll")
	}
	m.input.SetValue("Partial answer")
	m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	m.Update(doneMsg{})
	if m.input.Value() != "Original draft\nPartial answer" {
		t.Fatalf("lost draft: %q", m.input.Value())
	}
}

func TestQueueHeldWhenSuccessfulRunHasAnotherDraft(t *testing.T) {
	m := sizeModel(t, testModel())
	m.queued = "Next task"
	m.input.SetValue("Still composing")
	m.Update(doneMsg{})
	if m.state != stDone || m.queued != "Next task" || m.input.Value() != "Still composing" {
		t.Fatal("queue ran over draft")
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlE})
	if m.input.Value() != "Still composing" || m.queued != "Next task" {
		t.Fatal("queue editing overwrote draft")
	}
}

func TestInvalidAttachmentPreservesSubmission(t *testing.T) {
	m := sizeModel(t, testModel())
	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m.ws = ws
	m.state = stDone
	draft := `Inspect @"missing-file.png"`
	m.input.SetValue(draft)
	m.followUp()
	if m.input.Value() != draft {
		t.Fatal("failed attachment erased task")
	}
}

func TestPromptTransitionsFitWideAndNarrowTerminals(t *testing.T) {
	for _, width := range []int{40, 80, 120} {
		m := sizeModel(t, testModel())
		m.width(tea.WindowSizeMsg{Width: width, Height: 20})
		m.handleEvent(api.Event{Name: "awaiting_input", Fields: map[string]any{"kind": "permission", "prompt": "Review target"}})
		m.gstage = gsReject
		m.fitBottom()
		if !strings.Contains(m.View(), "note>") {
			t.Fatalf("note hidden at width %d", width)
		}
		m.handleEvent(api.Event{Name: "input_answered"})
		if m.sidebarVisible() && m.vp.Width != width-31 {
			t.Fatalf("chat did not refit: %d", m.vp.Width)
		}
	}
}

func TestBrowserActionsRemainInsideTerminalFrame(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	m.sessions = &sessionsState{entries: []sessionEntry{{id: "a", title: "Saved chat"}}}
	m.fitBottom()
	if !strings.Contains(m.View(), "Ctrl+R") || !strings.Contains(m.View(), "Ctrl+D") {
		t.Fatal("browser action bar clipped")
	}
}

func TestInlineThinkingUsesThinkingBlockLiveAndOnReopen(t *testing.T) {
	content := "<think>Проверяю ответ.\nSecond line.</think>\n\nI'm TARS."
	m := sizeModel(t, testModel())
	m.handleEvent(api.Event{Name: "step", Fields: map[string]any{"text": content}})
	history := renderHistory([]llm.Message{{Role: llm.RoleAssistant, Content: content}})
	for _, blocks := range [][]block{m.blocks, history} {
		if len(blocks) != 2 || blocks[0].role != roleThink || blocks[1].role != roleAnswer {
			t.Fatalf("incorrect reasoning split: %+v", blocks)
		}
		if blocks[0].text != "Проверяю ответ.\nSecond line." || blocks[1].text != "I'm TARS." {
			t.Fatalf("content changed: %+v", blocks)
		}
	}
	if strings.Contains(m.View(), "<think>") || !strings.Contains(m.View(), "[THINKING]") {
		t.Fatal("raw thinking shown as response")
	}
}

func TestThinkingSplitSupportsIncompleteAndMultipleRegions(t *testing.T) {
	for _, tc := range []struct{ content, reasoning, thought, answer string }{
		{"<think>unfinished", "", "unfinished", ""},
		{"<think></think>Answer", "", "", "Answer"},
		{"<think>first</think>Answer<think>second</think>", "", "first\n\nsecond", "Answer"},
		{"<think>inline</think>Answer", "separate", "separate\n\ninline", "Answer"},
	} {
		blocks := assistantBlocks(tc.content, tc.reasoning)
		var thought, answer string
		for _, b := range blocks {
			if b.role == roleThink {
				thought = b.text
			}
			if b.role == roleAnswer {
				answer = b.text
			}
		}
		if thought != tc.thought || answer != tc.answer {
			t.Fatalf("%q: thought %q, answer %q", tc.content, thought, answer)
		}
	}
}

func TestSystemNoticesShareConversationColumns(t *testing.T) {
	for _, tc := range []struct {
		b     block
		label string
	}{
		{markerBlock("— new task —"), "SYS"},
		{markerBlock("[harness] " + strings.Repeat("review this carefully ", 20)), "SYS"},
		{errorBlock("Cannot resume\nTry again"), "ERR"},
		{findingBlock("gofmt", "a.go", 3, "format required"), "WARN"},
		{gateBlock("Allow this tool?"), "ASK"},
	} {
		for _, width := range []int{30, 80, 120} {
			rendered := ansi.Strip(renderBlock(tc.b, defaultStyles(), false, width))
			rows := strings.Split(rendered, "\n")
			if !strings.HasPrefix(rows[0], fmt.Sprintf("%-6s", tc.label)) {
				t.Fatalf("label offset: %q", rows[0])
			}
			for _, row := range rows {
				if lipgloss.Width(row) > width {
					t.Fatalf("notice overflow: %q", row)
				}
			}
			for _, row := range rows[1:] {
				if row != "" && !strings.HasPrefix(row, speakerRail) {
					t.Fatalf("body misaligned: %q", row)
				}
			}
		}
	}
}

func TestHarnessNoticeMatchesLiveAndRebuiltHistory(t *testing.T) {
	text := "[harness] Verify your work.\n (step 2/25, 23 left)"
	m := sizeModel(t, testModel())
	m.handleEvent(api.Event{Name: "nudge", Fields: map[string]any{"kind": "verify", "text": text}})
	rebuilt := renderHistory([]llm.Message{{Role: llm.RoleUser, Content: text}})
	if len(m.blocks) != 1 || len(rebuilt) != 1 || rebuilt[0].role != roleMarker {
		t.Fatalf("harness omitted: %+v", rebuilt)
	}
	live := ansi.Strip(renderBlock(m.blocks[0], defaultStyles(), false, 80))
	restored := ansi.Strip(renderBlock(rebuilt[0], defaultStyles(), false, 80))
	if live != restored || !strings.HasPrefix(restored, "SYS   [harness]") || !strings.Contains(restored, "\n"+speakerRail+"(step 2/25, 23 left)") {
		t.Fatalf("harness rendering differs: %q / %q", live, restored)
	}
}
