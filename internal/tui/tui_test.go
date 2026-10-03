package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/keshon/tars/internal/agent"
	"github.com/keshon/tars/internal/api"
	"github.com/keshon/tars/internal/events"
	"github.com/keshon/tars/internal/llm"
	"github.com/keshon/tars/internal/permission"
	"github.com/keshon/tars/internal/roles"
	"github.com/keshon/tars/internal/tools"
	"github.com/keshon/tars/internal/workspace"
)

func testModel() *model {
	m := &model{
		events:  make(chan api.Event, 16),
		hub:     api.NewGateHub(nil),
		cancel:  func() {},
		started: time.Now(),
		styles:  defaultStyles(),
		follow:  true,
		note:    newNote(),
		always:  map[[2]string]bool{},
	}
	m.input = newInput()
	m.input.Prompt = "> "
	m.input.Focus()
	return m
}

func sizeModel(t *testing.T, m *model) *model {
	t.Helper()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	mm, ok := updated.(*model)
	if !ok {
		t.Fatal("Update did not return *model")
	}
	return mm
}

func TestStepAppendsTranscript(t *testing.T) {
	m := sizeModel(t, testModel())
	updated, _ := m.Update(eventMsg(api.Event{
		Name:   "step",
		Fields: map[string]any{"text": "hello", "step": 0},
	}))
	mm := updated.(*model)
	if len(mm.blocks) != 1 || mm.blocks[0].text != "hello" || mm.blocks[0].role != roleAnswer {
		t.Fatalf("blocks = %+v", mm.blocks)
	}
	if mm.steps != 1 {
		t.Fatalf("steps = %d", mm.steps)
	}
}

func TestStepReasoningRendersDim(t *testing.T) {
	m := sizeModel(t, testModel())
	updated, _ := m.Update(eventMsg(api.Event{
		Name:   "step",
		Fields: map[string]any{"text": "done", "reasoning": "hmm, tricky"},
	}))
	mm := updated.(*model)
	if len(mm.blocks) != 2 {
		t.Fatalf("blocks = %v", mm.blocks)
	}
	if mm.blocks[1].role != roleThink {
		t.Fatalf("reasoning must be a think block: %+v", mm.blocks[1])
	}
	if !strings.Contains(mm.blocks[1].text, "hmm, tricky") {
		t.Fatalf("reasoning not kept: %v", mm.blocks)
	}
}

func TestToolCallAndResultRender(t *testing.T) {
	m := sizeModel(t, testModel())
	updated, _ := m.Update(eventMsg(api.Event{
		Name: "step",
		Fields: map[string]any{
			"tool_calls": []any{map[string]any{"name": "read_file", "args": `{"path":"a"}`}},
		},
	}))
	mm := updated.(*model)
	if len(mm.blocks) != 1 || !strings.Contains(mm.blocks[0].text, "read_file") || mm.blocks[0].role != roleTool {
		t.Fatalf("blocks = %+v", mm.blocks)
	}
	updated, _ = mm.Update(eventMsg(api.Event{
		Name:   "tool_result",
		Fields: map[string]any{"call_id": "c1", "text": "-old\n+new"},
	}))
	mm = updated.(*model)
	if len(mm.blocks) != 2 {
		t.Fatalf("blocks = %v", mm.blocks)
	}
}

func TestPermissionKeysAnswer(t *testing.T) {
	m := sizeModel(t, testModel())
	updated, _ := m.Update(eventMsg(api.Event{
		Name:   "awaiting_input",
		Fields: map[string]any{"kind": "permission", "prompt": "allow?"},
	}))
	mm := updated.(*model)
	if mm.state != stPermission {
		t.Fatalf("state = %v", mm.state)
	}
	// Suspend in the background: key handling must wake it.
	answered := make(chan agent.SuspendReply, 1)
	go func() {
		rep, err := mm.hub.Suspender()(t.Context(), agent.SuspendRequest{Kind: agent.SuspendPermission})
		if err != nil {
			return
		}
		answered <- rep
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !mm.hub.HasPending() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	mm = updated.(*model)
	if mm.state != stRunning {
		t.Fatalf("state = %v after y", mm.state)
	}
	select {
	case rep := <-answered:
		if rep.Answer != "y" {
			t.Fatalf("reply = %+v", rep)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("suspender never woke")
	}
}

func TestAskEnterSubmits(t *testing.T) {
	m := sizeModel(t, testModel())
	updated, _ := m.Update(eventMsg(api.Event{
		Name:   "awaiting_input",
		Fields: map[string]any{"kind": "ask_user", "prompt": "which?"},
	}))
	mm := updated.(*model)
	if mm.state != stAsk {
		t.Fatalf("state = %v", mm.state)
	}
	answered := make(chan agent.SuspendReply, 1)
	go func() {
		rep, err := mm.hub.Suspender()(t.Context(), agent.SuspendRequest{Kind: agent.SuspendAsk})
		if err != nil {
			return
		}
		answered <- rep
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !mm.hub.HasPending() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	mm.input.SetValue("assumed")
	updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm = updated.(*model)
	select {
	case rep := <-answered:
		if rep.Answer != "assumed" {
			t.Fatalf("reply = %+v", rep)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("suspender never woke")
	}
	if mm.state != stRunning {
		t.Fatalf("state = %v after enter", mm.state)
	}
}

func TestDoneQuits(t *testing.T) {
	m := sizeModel(t, testModel())
	updated, _ := m.Update(doneMsg{answer: "done"})
	mm := updated.(*model)
	if mm.state != stDone || mm.answer != "done" {
		t.Fatalf("state = %v answer = %q", mm.state, mm.answer)
	}
	// Empty follow-up quits; the program no longer exits on done.
	updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm = updated.(*model)
	if !mm.quit {
		t.Fatal("empty enter in done state must quit")
	}
}

func TestFollowUpResumes(t *testing.T) {
	m := sizeModel(t, testModel())
	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := api.New(api.Config{
		Task: "task",
		Env: roles.Env{
			Client: stubDone{},
			WS:     ws,
			Procs:  tools.NewBackgroundProcesses(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	m.sess = sess
	doneCh := make(chan doneMsg, 4)
	m.send = func(msg tea.Msg) {
		if d, ok := msg.(doneMsg); ok {
			doneCh <- d
		}
	}
	m.ctx = context.Background()
	stateFile := filepath.Join(t.TempDir(), "state.json")
	seedHistory(t, stateFile)
	m.stateFile = stateFile

	updated, _ := m.Update(doneMsg{answer: "done"})
	mm := updated.(*model)
	mm.input.SetValue("and then?")
	updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm = updated.(*model)
	if mm.state != stRunning {
		t.Fatalf("state = %v, want running", mm.state)
	}
	echoed := false
	for _, b := range mm.blocks {
		if strings.Contains(b.text, "and then?") {
			echoed = true
			if b.role != roleUser {
				t.Fatalf("echo must be a user block: %+v", b)
			}
		}
	}
	if !echoed {
		t.Fatalf("follow-up not echoed: %v", mm.blocks)
	}
	select {
	case d := <-doneCh:
		if d.answer != "done" {
			t.Fatalf("answer = %q", d.answer)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("resumed run never completed")
	}
}

func TestMeter_UsageAndUnknown(t *testing.T) {
	m := sizeModel(t, testModel())
	m.limit = 131072
	if got := m.meter(); got != "ctx —" {
		t.Fatalf("no measurement yet: %q", got)
	}
	updated, _ := m.Update(eventMsg(api.Event{
		Name:   "usage",
		Fields: map[string]any{"prompt": float64(12400)},
	}))
	mm := updated.(*model)
	if got := mm.meter(); got != "ctx 12.4k / 131.1k (9%)" {
		t.Fatalf("meter = %q", got)
	}
}

func TestKTokens(t *testing.T) {
	if got := kTokens(999); got != "999" {
		t.Fatalf("got %q", got)
	}
	if got := kTokens(12400); got != "12.4k" {
		t.Fatalf("got %q", got)
	}
}

func TestCommand_HelpAndUnknown(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	m.input.SetValue("/help")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := updated.(*model)
	if mm.quit || mm.state != stDone {
		t.Fatalf("help must not quit or resume: quit=%v state=%v", mm.quit, mm.state)
	}
	found := false
	for _, b := range mm.blocks {
		if strings.Contains(b.text, "/quit") {
			found = true
		}
	}
	if !found {
		t.Fatalf("help text missing: %v", mm.blocks)
	}

	mm.input.SetValue("/nope")
	updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm = updated.(*model)
	if mm.quit || mm.state != stDone {
		t.Fatalf("unknown command must not quit or resume")
	}
}

func TestCommand_Quit(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	m.input.SetValue("/quit")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if mm := updated.(*model); !mm.quit {
		t.Fatal("/quit must quit")
	}
}

func TestCommand_NewNeedsTask(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	m.input.SetValue("/new")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if mm := updated.(*model); mm.quit || mm.state != stDone {
		t.Fatal("bare /new must explain usage and stay")
	}
}

func TestCommand_NewStartsFresh(t *testing.T) {
	m := sizeModel(t, testModel())
	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m.newSession = func(task, stateFile string) (*api.Session, error) {
		if task != "second" {
			t.Errorf("task = %q", task)
		}
		if stateFile == "" || !strings.HasSuffix(stateFile, "state.json") {
			t.Errorf("stateFile = %q", stateFile)
		}
		return api.New(api.Config{
			Task: task,
			Env: roles.Env{
				Client: stubDone{},
				WS:     ws,
				Procs:  tools.NewBackgroundProcesses(),
			},
		})
	}
	var sent int32
	m.send = func(tea.Msg) { atomic.AddInt32(&sent, 1) }
	m.ctx = context.Background()
	m.state = stDone
	m.input.SetValue("/new second")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := updated.(*model)
	if mm.state != stRunning {
		t.Fatalf("state = %v, want running", mm.state)
	}
	if len(mm.blocks) != 1 {
		t.Fatalf("transcript not reset to the new-task marker: %v", mm.blocks)
	}
	deadline := time.Now().Add(10 * time.Second)
	for atomic.LoadInt32(&sent) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt32(&sent) == 0 {
		t.Fatal("fresh run never reported completion")
	}
}

func seedHistory(t *testing.T, path string) {
	t.Helper()
	history := []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "task"},
		{Role: llm.RoleAssistant, Content: "done"},
	}
	data, err := json.Marshal(history)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

type stubDone struct{}

func (stubDone) Chat(_ context.Context, _ llm.ChatRequest) (llm.ChatResponse, error) {
	return llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}}, nil
}

func TestRenderResultDiffColors(t *testing.T) {
	out := renderResult("-old\n+new\n@@ h @@\nctx", defaultStyles())
	for _, want := range []string{"-old", "+new", "@@ h @@", "ctx"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
}

func TestViewportSizedWithoutTTY(t *testing.T) {
	m := sizeModel(t, testModel())
	if !m.ready || m.vp.Width != 80 {
		t.Fatalf("viewport not sized: %+v", m.vp)
	}
}

func TestHistory_EdgeGated(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	m.pushHistory("first")
	m.pushHistory("second")

	// Up on a single-line input recalls.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	mm := updated.(*model)
	if mm.input.Value() != "second" {
		t.Fatalf("up recalled %q", mm.input.Value())
	}
	updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyUp})
	mm = updated.(*model)
	if mm.input.Value() != "first" {
		t.Fatalf("up again recalled %q", mm.input.Value())
	}
	// Down walks forward, past the end restores the draft.
	updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyDown})
	mm = updated.(*model)
	if mm.input.Value() != "second" {
		t.Fatalf("down recalled %q", mm.input.Value())
	}
	updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyDown})
	mm = updated.(*model)
	if mm.input.Value() != "" {
		t.Fatalf("down past end restored %q, want draft", mm.input.Value())
	}
}

func TestHistory_MultilineCaretGate(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	m.pushHistory("old")
	m.input.SetValue("line1\nline2")
	// Cursor lands at the end: first up moves within the text (no
	// recall), second up — now on the first line — recalls.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	mm := updated.(*model)
	if mm.input.Value() != "line1\nline2" {
		t.Fatalf("up on last line must move caret, got %q", mm.input.Value())
	}
	updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyUp})
	if mm := updated.(*model); mm.input.Value() != "old" {
		t.Fatalf("up on first line must recall, got %q", mm.input.Value())
	}
}

func TestHistory_DedupesConsecutive(t *testing.T) {
	m := sizeModel(t, testModel())
	m.pushHistory("x")
	m.pushHistory("x")
	m.pushHistory("y")
	if len(m.hist) != 2 {
		t.Fatalf("hist = %v", m.hist)
	}
}

func TestNewlineKey(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	m.input.SetValue("a")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	mm := updated.(*model)
	if !strings.Contains(mm.input.Value(), "\n") {
		t.Fatalf("ctrl+o must insert newline, got %q", mm.input.Value())
	}
}

func TestInterruptStaysInChat(t *testing.T) {
	m := sizeModel(t, testModel())
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	mm := updated.(*model)
	if mm.quit {
		t.Fatal("esc must interrupt, not quit")
	}
	if mm.state != stDone {
		t.Fatalf("state = %v, want done", mm.state)
	}
	// The cancelled run's doneMsg renders neutrally, not as an error.
	updated, _ = mm.Update(doneMsg{err: context.Canceled})
	mm = updated.(*model)
	if mm.runErr != nil {
		t.Fatalf("runErr = %v, want nil after interrupt", mm.runErr)
	}
	found := false
	for _, b := range mm.blocks {
		if strings.Contains(b.text, "interrupted") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no interrupt marker: %v", mm.blocks)
	}
}

func TestFollowUnfollowsOnScroll(t *testing.T) {
	m := sizeModel(t, testModel())
	for i := 0; i < 40; i++ {
		m.appendBlock(markerBlock(strings.Repeat("line ", 20) + string(rune('a'+i%26))))
	}
	if !m.follow {
		t.Fatal("follow expected while at bottom")
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	mm := updated.(*model)
	if mm.follow {
		t.Fatal("pgup must unfollow")
	}
	if !strings.Contains(mm.View(), "↓ end") {
		t.Fatal("unfollowed state must show jump hint")
	}
	updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyEnd})
	mm = updated.(*model)
	if !mm.follow {
		t.Fatal("end must re-arm follow")
	}
}

func TestNewBlocksRespectUnfollow(t *testing.T) {
	m := sizeModel(t, testModel())
	for i := 0; i < 40; i++ {
		m.appendBlock(markerBlock("block"))
	}
	m.follow = false
	y0 := m.vp.YOffset
	m.appendBlock(markerBlock("new block while reading"))
	if m.vp.YOffset != y0 {
		t.Fatal("new block yanked a scrolled-up viewport")
	}
}

func TestQuitLetterNeedsEmptyBox(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	m.input.SetValue("quit")
	q := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}
	updated, _ := m.Update(q)
	mm := updated.(*model)
	if mm.quit {
		t.Fatal("typing q into non-empty input must not quit")
	}
	if mm.input.Value() != "quitq" {
		t.Fatalf("keystroke must still reach the input, got %q", mm.input.Value())
	}
	// Empty (even whitespace-only) box: q quits.
	mm.input.SetValue("   ")
	updated, _ = mm.Update(q)
	if mm := updated.(*model); !mm.quit {
		t.Fatal("q on empty input must quit")
	}
}

func TestScrollRoutedInEveryState(t *testing.T) {
	for _, state := range []runState{stRunning, stAsk, stDone} {
		m := sizeModel(t, testModel())
		for i := 0; i < 40; i++ {
			m.appendBlock(markerBlock("block"))
		}
		if !m.follow {
			t.Fatalf("state %v: follow expected while at bottom", state)
		}
		m.state = state
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
		if mm := updated.(*model); mm.follow {
			t.Fatalf("state %v: pgup must unfollow", state)
		}
	}
	// Wheel scrolls outside the running state too.
	m := sizeModel(t, testModel())
	for i := 0; i < 40; i++ {
		m.appendBlock(markerBlock("block"))
	}
	m.state = stDone
	wheel := tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress}
	updated, _ := m.Update(wheel)
	if mm := updated.(*model); mm.follow {
		t.Fatal("wheel-up in done state must unfollow")
	}
}

func TestSpinnerOnlyWhileRunning(t *testing.T) {
	m := testModel()
	m.state = stRunning
	m.elapsed = 0
	if got := m.spinner(); got != "| " {
		t.Fatalf("spinner = %q", got)
	}
	m.elapsed = 2 * time.Second
	if got := m.spinner(); got != "- " {
		t.Fatalf("spinner = %q, want frame advance", got)
	}
	m.state = stDone
	if got := m.spinner(); got != "" {
		t.Fatalf("spinner outside running = %q", got)
	}
	// The running status line carries the frame.
	m = sizeModel(t, testModel())
	m.state = stRunning
	m.elapsed = 0
	if view := m.View(); !strings.Contains(view, "| running") {
		t.Fatalf("status line missing spinner: %q", view)
	}
}

func TestEmptyStart_SubmitBegins(t *testing.T) {
	m := sizeModel(t, testModel())
	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m.newSession = func(task, stateFile string) (*api.Session, error) {
		return api.New(api.Config{
			Task: task,
			Env: roles.Env{
				Client: stubDone{},
				WS:     ws,
				Procs:  tools.NewBackgroundProcesses(),
			},
		})
	}
	sent := make(chan doneMsg, 4)
	m.send = func(msg tea.Msg) {
		if d, ok := msg.(doneMsg); ok {
			sent <- d
		}
	}
	m.ctx = context.Background()
	// No session, no state: first line starts fresh, like /new.
	m.state = stDone
	m.input.SetValue("hello?")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := updated.(*model)
	if mm.state != stRunning {
		t.Fatalf("state = %v, want running", mm.state)
	}
	if mm.sess == nil || mm.stateFile == "" {
		t.Fatal("fresh session and state file expected")
	}
	// The typed task echoes as the opening user block, after the
	// new-task marker (startFresh resets the transcript first).
	if len(mm.blocks) != 2 || mm.blocks[1].role != roleUser {
		t.Fatalf("opening task must echo as user block: %+v", mm.blocks)
	}
	if !strings.Contains(mm.blocks[1].text, "❯ hello?") {
		t.Fatalf("echo text = %q", mm.blocks[1].text)
	}
	select {
	case d := <-sent:
		if d.answer != "done" {
			t.Fatalf("answer = %q", d.answer)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("fresh run never completed")
	}
}

func TestRolesAssignedAcrossEvent(t *testing.T) {
	m := sizeModel(t, testModel())
	updated, _ := m.Update(eventMsg(api.Event{
		Name: "step",
		Fields: map[string]any{
			"text":      "answer",
			"reasoning": "...musing about the task",
			"tool_calls": []any{
				map[string]any{"name": "read_file", "args": "{}"},
			},
		},
	}))
	mm := updated.(*model)
	want := []role{roleAnswer, roleThink, roleTool}
	if len(mm.blocks) != len(want) {
		t.Fatalf("blocks = %+v", mm.blocks)
	}
	for i, r := range want {
		if mm.blocks[i].role != r {
			t.Fatalf("block %d role = %v, want %v", i, mm.blocks[i].role, r)
		}
	}
}

func TestThinkCollapsedByDefault(t *testing.T) {
	st := defaultStyles()
	long := "...The user is asking something, and here is a long chain of internal deliberation that must not read as the answer. " +
		"Second line of musing that only the expanded view may show."
	collapsed := renderBlock(thinkBlock(long), st, false)
	if !strings.Contains(collapsed, "⋯") {
		t.Fatalf("collapsed thinking needs its marker: %q", collapsed)
	}
	if strings.Contains(collapsed, "Second line of musing") {
		t.Fatalf("collapsed thinking leaks body: %q", collapsed)
	}
	if !strings.Contains(collapsed, thinkToggleHint) {
		t.Fatalf("collapsed thinking must name its key: %q", collapsed)
	}
	expanded := renderBlock(thinkBlock(long), st, true)
	if !strings.Contains(expanded, "Second line of musing") {
		t.Fatalf("expanded thinking must show all: %q", expanded)
	}
}

func TestGutterFixedWidth(t *testing.T) {
	for _, r := range allRoles {
		if got := lipgloss.Width(gutterGlyph(r)); got != gutterWidth {
			t.Fatalf("role %v gutter width = %d, want %d", r, got, gutterWidth)
		}
	}
}

func TestThinkToggleKey(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	if m.showThink {
		t.Fatal("thinking starts collapsed")
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlG})
	if mm := updated.(*model); !mm.showThink {
		t.Fatal("ctrl+g must expand thinking")
	} else {
		updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyCtrlG})
		if mm := updated.(*model); mm.showThink {
			t.Fatal("ctrl+g again must collapse")
		}
	}
	// A plain "t" types, never toggles.
	mm := sizeModel(t, testModel())
	mm.state = stDone
	mm.input.SetValue("ed")
	updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	if mm := updated.(*model); mm.showThink || mm.input.Value() != "edt" {
		t.Fatalf("t must type, not toggle: showThink=%v value=%q", mm.showThink, mm.input.Value())
	}
}

func TestHelpMentionsThinkKey(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	m.input.SetValue("/help")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := updated.(*model)
	found := false
	for _, b := range mm.blocks {
		if strings.Contains(b.text, thinkToggleHint) {
			found = true
		}
	}
	if !found {
		t.Fatalf("help must document the think key: %+v", mm.blocks)
	}
}

// openPermissionGate drives a permission suspension the way the run
// worker would, returning the model with the gate overlay open.
func openPermissionGate(t *testing.T) (*model, chan agent.SuspendReply) {
	t.Helper()
	m := sizeModel(t, testModel())
	updated, _ := m.Update(eventMsg(api.Event{
		Name: "awaiting_input",
		Fields: map[string]any{
			"kind": "permission", "prompt": "allow?",
			"tool": "run_shell", "resource": "go test",
		},
	}))
	mm := updated.(*model)
	if mm.state != stPermission || mm.gstage != gsPermit {
		t.Fatalf("state = %v stage = %v", mm.state, mm.gstage)
	}
	if len(mm.overlays) != 1 {
		t.Fatalf("gate must push one overlay, have %d", len(mm.overlays))
	}
	answered := make(chan agent.SuspendReply, 1)
	go func() {
		rep, err := mm.hub.Suspender()(t.Context(), agent.SuspendRequest{Kind: agent.SuspendPermission})
		if err != nil {
			return
		}
		answered <- rep
	}()
	waitPending(t, mm.hub)
	return mm, answered
}

func waitPending(t *testing.T, hub *api.GateHub) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !hub.HasPending() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !hub.HasPending() {
		t.Fatal("gate never went pending")
	}
}

func awaitReply(t *testing.T, ch chan agent.SuspendReply) agent.SuspendReply {
	t.Helper()
	select {
	case rep := <-ch:
		return rep
	case <-time.After(2 * time.Second):
		t.Fatal("suspender never woke")
		return agent.SuspendReply{}
	}
}

func keyRunes(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

func TestGateStages_AlwaysConfirm(t *testing.T) {
	mm, answered := openPermissionGate(t)
	// "a" escalates to confirm, answering nothing yet.
	updated, _ := mm.Update(keyRunes('a'))
	mm = updated.(*model)
	if mm.gstage != gsAlways || mm.state != stPermission {
		t.Fatalf("stage = %v state = %v, want confirm", mm.gstage, mm.state)
	}
	if !mm.hub.HasPending() {
		t.Fatal("confirm stage must not answer yet")
	}
	if got := mm.gateBar(); !strings.Contains(got, "run_shell") {
		t.Fatalf("confirm must name the gated call: %q", got)
	}
	// Esc backs out, still pending.
	updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyEsc})
	mm = updated.(*model)
	if mm.gstage != gsPermit || !mm.hub.HasPending() {
		t.Fatal("esc must return to permit, still pending")
	}
	// Enter on confirm answers "a" and tears the overlay down.
	updated, _ = mm.Update(keyRunes('a'))
	mm = updated.(*model)
	updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm = updated.(*model)
	if rep := awaitReply(t, answered); rep.Answer != "a" {
		t.Fatalf("reply = %+v", rep)
	}
	if mm.state != stRunning || len(mm.overlays) != 0 {
		t.Fatalf("state = %v overlays = %d, want teardown", mm.state, len(mm.overlays))
	}
}

func TestGateStages_RejectNote(t *testing.T) {
	mm, answered := openPermissionGate(t)
	// "n" drops to the note stage, answering nothing yet.
	updated, _ := mm.Update(keyRunes('n'))
	mm = updated.(*model)
	if mm.gstage != gsReject || !mm.hub.HasPending() {
		t.Fatal("n must open the note stage, still pending")
	}
	if !mm.note.Focused() {
		t.Fatal("note must take focus")
	}
	for _, r := range "use cat" {
		updated, _ = mm.Update(keyRunes(r))
		mm = updated.(*model)
	}
	updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm = updated.(*model)
	if rep := awaitReply(t, answered); rep.Answer != "n: use cat" {
		t.Fatalf("reply = %+v", rep)
	}
	if mm.state != stRunning || len(mm.overlays) != 0 {
		t.Fatalf("state = %v overlays = %d, want teardown", mm.state, len(mm.overlays))
	}
	found := false
	for _, b := range mm.blocks {
		if b.role == roleUser && strings.Contains(b.text, "use cat") {
			found = true
		}
	}
	if !found {
		t.Fatalf("note must echo as user block: %+v", mm.blocks)
	}
}

func TestGateRejectEmptyNoteDenies(t *testing.T) {
	mm, answered := openPermissionGate(t)
	updated, _ := mm.Update(keyRunes('n'))
	mm = updated.(*model)
	updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if rep := awaitReply(t, answered); rep.Answer != "n" {
		t.Fatalf("reply = %+v, want plain deny", rep)
	}
}

func TestGateHook_AlwaysMemory(t *testing.T) {
	m := sizeModel(t, testModel())
	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hook := m.gateHook(m.hub, context.Background(), ws)
	type result struct {
		eff permission.Effect
		err error
	}
	first := make(chan result, 1)
	go func() {
		eff, err := hook("read_file", "notes.txt", json.RawMessage(`{}`))
		first <- result{eff, err}
	}()
	waitPending(t, m.hub)
	if err := m.hub.Respond("a"); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-first:
		if r.eff != permission.Allow || r.err != nil {
			t.Fatalf("first = %v, %v", r.eff, r.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hook never returned")
	}
	// Repeat: auto-allowed with no suspension.
	if eff, err := hook("read_file", "notes.txt", json.RawMessage(`{}`)); eff != permission.Allow || err != nil {
		t.Fatalf("repeat = %v, %v", eff, err)
	}
	if m.hub.HasPending() {
		t.Fatal("repeat must not suspend")
	}
	// "y" allows once: the same pair suspends again next time.
	second := make(chan result, 1)
	go func() {
		eff, err := hook("read_file", "other.txt", json.RawMessage(`{}`))
		second <- result{eff, err}
	}()
	waitPending(t, m.hub)
	if err := m.hub.Respond("y"); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-second:
		if r.eff != permission.Allow || r.err != nil {
			t.Fatalf("second = %v, %v", r.eff, r.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hook never returned")
	}
	third := make(chan result, 1)
	go func() {
		eff, err := hook("read_file", "other.txt", json.RawMessage(`{}`))
		third <- result{eff, err}
	}()
	waitPending(t, m.hub)
	if err := m.hub.Respond("y"); err != nil {
		t.Fatal(err)
	}
	<-third
}

func TestOverlayPushPopRestore(t *testing.T) {
	m := sizeModel(t, testModel())
	if !m.input.Focused() {
		t.Fatal("test setup: input must start focused")
	}
	m.pushOverlay(ovGate)
	m.applyFocus(focusNote)
	if m.input.Focused() || !m.note.Focused() {
		t.Fatal("applyFocus(note) must move focus")
	}
	m.popOverlay()
	if !m.input.Focused() || m.note.Focused() {
		t.Fatal("pop must restore input focus")
	}
	if len(m.overlays) != 0 {
		t.Fatal("stack must drain")
	}
	m.popOverlay() // unbalanced pop: no-op, never a panic
}

func TestTimestampRendered(t *testing.T) {
	st := defaultStyles()
	at := time.Date(2026, 10, 3, 14, 22, 0, 0, time.Local)
	out := renderBlock(block{roleUser, "❯ hi", at}, st, false)
	if !strings.Contains(out, "[14:22]") {
		t.Fatalf("user block missing timestamp: %q", out)
	}
	// Unstamped blocks (zero time) render cleanly for unit-built models.
	plain := renderBlock(block{roleAnswer, "hi", time.Time{}}, st, false)
	if strings.Contains(plain, "[") {
		t.Fatalf("zero-time block must not stamp: %q", plain)
	}
}

func TestInitialTaskEchoedAsUser(t *testing.T) {
	m := sizeModel(t, testModel())
	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m.newSession = func(task, stateFile string) (*api.Session, error) {
		return api.New(api.Config{
			Task: task,
			Env: roles.Env{
				Client: stubDone{},
				WS:     ws,
				Procs:  tools.NewBackgroundProcesses(),
			},
		})
	}
	m.send = func(tea.Msg) {}
	m.ctx = context.Background()
	if err := m.startInitial("  hi  "); err != nil {
		t.Fatal(err)
	}
	if m.state != stRunning {
		t.Fatalf("state = %v, want running", m.state)
	}
	if len(m.blocks) != 1 || m.blocks[0].role != roleUser {
		t.Fatalf("opening task must echo as user block: %+v", m.blocks)
	}
	if !strings.Contains(m.blocks[0].text, "❯ hi") {
		t.Fatalf("echo must trim the task: %q", m.blocks[0].text)
	}
}

func TestLongStepFlowsUncut(t *testing.T) {
	// P10-2 fixture (TUI-2/TUI-10): a long verification-list reply
	// plus multibyte reasoning must survive the event funnel intact.
	text := strings.Repeat("verify this. ", 150)
	reason := strings.Repeat("deliberation — ", 100)
	var buf bytes.Buffer
	em := events.New(&buf)
	api.EmitStep(em, "run", 1, llm.Message{Content: text, Reasoning: reason})
	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec); err != nil {
		t.Fatalf("not json: %v", err)
	}
	fields := map[string]any{}
	for k, v := range rec {
		if k != "seq" && k != "event" {
			fields[k] = v
		}
	}
	m := sizeModel(t, testModel())
	updated, _ := m.Update(eventMsg(api.Event{Name: "step", Fields: fields}))
	mm := updated.(*model)
	if len(mm.blocks) != 2 {
		t.Fatalf("blocks = %d", len(mm.blocks))
	}
	if mm.blocks[0].role != roleAnswer || mm.blocks[0].text != text {
		t.Fatal("answer cut or misroled by the funnel")
	}
	if mm.blocks[1].role != roleThink || mm.blocks[1].text != reason {
		t.Fatal("reasoning cut or misroled by the funnel")
	}
}

func TestFormatElapsed(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{0, "00:00"},
		{40 * time.Second, "00:40"},
		{70 * time.Second, "01:10"},
		{3723 * time.Second, "1:02:03"},
		{-5 * time.Second, "00:00"},
	} {
		if got := formatElapsed(tc.in); got != tc.want {
			t.Fatalf("formatElapsed(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestElapsedRunsInFlightFreezesWhenDone(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stRunning
	m.started = time.Now().Add(-90 * time.Second)
	updated, _ := m.Update(tickMsg(time.Now()))
	mm := updated.(*model)
	if mm.elapsed != 90*time.Second {
		t.Fatalf("running elapsed = %v, want 1m30s", mm.elapsed)
	}
	// Gates are still the run's wall clock.
	mm.state = stAsk
	mm.started = time.Now().Add(-5 * time.Second)
	updated, _ = mm.Update(tickMsg(time.Now()))
	if mm := updated.(*model); mm.elapsed != 5*time.Second {
		t.Fatalf("ask elapsed = %v, want 5s", mm.elapsed)
	}
	// Done freezes: idle reading time never accrues.
	mm.state = stDone
	updated, _ = mm.Update(tickMsg(time.Now()))
	if mm := updated.(*model); mm.elapsed != 5*time.Second {
		t.Fatalf("done elapsed moved to %v", mm.elapsed)
	}
}

func TestStartRunResetsClock(t *testing.T) {
	m := sizeModel(t, testModel())
	m.send = func(tea.Msg) {}
	m.ctx = context.Background()
	m.started = time.Now().Add(-time.Hour)
	m.steps = 7
	m.tokens = 42
	before := time.Now()
	m.startRun(func(ctx context.Context) (string, error) { return "done", nil })
	if m.started.Before(before) {
		t.Fatal("startRun must reset the clock")
	}
	if m.steps != 0 || m.tokens != 0 || m.state != stRunning {
		t.Fatalf("run not reset: steps=%d tokens=%d state=%v", m.steps, m.tokens, m.state)
	}
}
