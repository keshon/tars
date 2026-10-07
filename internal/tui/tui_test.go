package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/keshon/tars/internal/agent"
	"github.com/keshon/tars/internal/api"
	"github.com/keshon/tars/internal/audit"
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
		compact: true, maxSteps: agent.DefaultMaxSteps,
		note:   newNote(),
		always: map[[2]string]bool{},
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
	if mm.blocks[0].role != roleThink {
		t.Fatalf("reasoning must be a think block: %+v", mm.blocks[0])
	}
	if !strings.Contains(mm.blocks[0].text, "hmm, tricky") {
		t.Fatalf("reasoning not kept: %v", mm.blocks)
	}
	if mm.blocks[1].role != roleAnswer {
		t.Fatalf("answer must follow its thinking: %+v", mm.blocks[1])
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
	updated, _ = mm.Update(tea.KeyPressMsg{Text: string([]rune{'y'})})
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
	updated, _ = mm.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
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
	updated, _ = mm.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm = updated.(*model)
	if mm.quit {
		t.Fatal("empty enter in done state must do nothing")
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
	updated, _ = mm.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
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

func TestMeter_Estimated(t *testing.T) {
	m := sizeModel(t, testModel())
	m.limit = 131072
	updated, _ := m.Update(eventMsg(api.Event{
		Name:   "usage",
		Fields: map[string]any{"prompt": float64(12400), "estimated": true},
	}))
	mm := updated.(*model)
	if got := mm.meter(); got != "~ctx 12.4k / 131.1k (9%)" {
		t.Fatalf("estimated meter = %q", got)
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
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm := updated.(*model)
	if mm.quit || mm.state != stDone {
		t.Fatalf("help must not quit or resume: quit=%v state=%v", mm.quit, mm.state)
	}
	if mm.dialog == nil || !mm.dialog.help {
		t.Fatal("/help must open the shared help modal")
	}
	mm.dialog.page = 3
	for _, want := range []string{"/help", "/sessions"} {
		if !strings.Contains(mm.dialogView(), want) {
			t.Fatalf("help missing %q", want)
		}
	}
	// A second unrelated command works on its own model state.
	mm2 := sizeModel(t, testModel())
	mm2.state = stDone
	mm2.input.SetValue("/nope")
	updated, _ = mm2.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if mm := updated.(*model); mm.quit || mm.state != stDone {
		t.Fatal("unknown command must not quit or resume")
	}
}

func TestCommand_Quit(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	m.input.SetValue("/quit")
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if mm := updated.(*model); !mm.quit {
		t.Fatal("/quit must quit")
	}
}

func TestCommand_BareNewResetsToChat(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	m.blocks = []block{answerBlock("old transcript")}
	m.stateFile = "some/state.json"
	m.input.SetValue("/new")
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm := updated.(*model)
	if mm.quit || mm.state != stDone {
		t.Fatal("bare /new must reset and stay")
	}
	if mm.sess != nil || mm.stateFile != "" {
		t.Fatal("bare /new must drop the session")
	}
	if len(mm.blocks) != 1 || !strings.Contains(mm.blocks[0].text, "New chat") {
		t.Fatalf("blocks = %+v, want a clean transcript with one marker", mm.blocks)
	}
	// The next line starts a fresh run, not a follow-up to nothing:
	// with no session, followUp must take the startFresh path.
	mm.newSession = func(task, stateFile string, images []string) (*api.Session, error) {
		return nil, errors.New("fresh path taken")
	}
	mm.input.SetValue("fresh task")
	updated, _ = mm.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm2 := updated.(*model)
	found := false
	for _, b := range mm2.blocks {
		if strings.Contains(b.text, "cannot start: fresh path taken") {
			found = true
		}
	}
	if !found {
		t.Fatalf("blocks = %+v, want the startFresh error path", mm2.blocks)
	}
}

// A fresh follow-up with @path reaches the session constructor with
// the text cleaned, and the echo marks the attachment.
func TestFollowUp_AttachesImages(t *testing.T) {
	m := sizeModel(t, testModel())
	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Root(), "shot.png"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.ws = ws
	var gotTask string
	var gotImages []string
	m.newSession = func(task, stateFile string, images []string) (*api.Session, error) {
		gotTask, gotImages = task, images
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
	m.input.SetValue("what is this @shot.png")
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm := updated.(*model)
	if gotTask != "what is this" || len(gotImages) != 1 {
		t.Fatalf("task=%q images=%v", gotTask, gotImages)
	}
	found := false
	for _, b := range mm.blocks {
		if strings.Contains(b.text, "[image: shot.png]") {
			found = true
		}
	}
	if !found {
		t.Fatalf("echo missing image marker: %+v", mm.blocks)
	}
}

func TestCommand_NewStartsFresh(t *testing.T) {
	m := sizeModel(t, testModel())
	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m.newSession = func(task, stateFile string, images []string) (*api.Session, error) {
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
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm := updated.(*model)
	if mm.state != stRunning {
		t.Fatalf("state = %v, want running", mm.state)
	}
	if len(mm.blocks) != 2 || mm.blocks[0].role != roleMarker || mm.blocks[1].role != roleUser {
		t.Fatalf("transcript must open marker + task echo: %+v", mm.blocks)
	}
	if mm.blocks[1].text != "second" {
		t.Fatalf("echo = %q, want verbatim task", mm.blocks[1].text)
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

func TestResultBlockDiffColors(t *testing.T) {
	out := renderBlock(resultBlock("-old\n+new\n@@ h @@\nctx"), defaultStyles(), false, 80)
	for _, want := range []string{"-old", "+new", "@@ h @@", "ctx"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
}

// A trailing newline in answer text is content termination, not a blank
// line: joined with the uniform block gap it must not double.
func TestAnswerTrailingNewlineNoDoubleGap(t *testing.T) {
	st := defaultStyles()
	ans := answerBlock("I'll explore my own workspace.\n")
	tool := toolCardBlock("c1", "read_file")
	tool.result = "ok"
	tool.open = false
	joined := renderBlock(ans, st, false, 80) + "\n\n" +
		renderBlock(tool, st, false, 80)
	if gap := consecutiveBlanks(joined); gap > 1 {
		t.Fatalf("double gap after reply with trailing newline:\n%q", joined)
	}
	if ans.text != "I'll explore my own workspace.\n" {
		t.Fatalf("stored text mutated: %q", ans.text)
	}
}

// consecutiveBlanks counts the longest run of visually blank lines
// (ANSI gutter + whitespace only). The block join contributes exactly
// one; anything more is a rendering defect.
func consecutiveBlanks(s string) int {
	ansi := regexp.MustCompile("\x1b\\[[0-9;]*m")
	best, run := 0, 0
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(ansi.ReplaceAllString(line, "")) == "" {
			run++
			if run > best {
				best = run
			}
		} else {
			run = 0
		}
	}
	return best
}

func TestViewportSizedWithoutTTY(t *testing.T) {
	m := sizeModel(t, testModel())
	if !m.ready || m.vp.Width() != 80 {
		t.Fatalf("viewport not sized: %+v", m.vp)
	}
}

func TestHistory_ExplicitRecallRestoresDraft(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	m.pushHistory("first")
	m.pushHistory("second")
	m.input.SetValue("draft\nsecond line")
	for _, want := range []string{"second", "first"} {
		m.Update(tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModAlt})
		if m.input.Value() != want {
			t.Fatalf("recall %q, want %q", m.input.Value(), want)
		}
	}
	for _, want := range []string{"second", "draft\nsecond line"} {
		m.Update(tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModAlt})
		if m.input.Value() != want {
			t.Fatalf("recall %q, want %q", m.input.Value(), want)
		}
	}
}

func TestHistory_ArrowsNeverRecallAtEditorEdges(t *testing.T) {
	for _, state := range []runState{stDone, stAsk, stRunning} {
		m := sizeModel(t, testModel())
		m.state = state
		m.pushHistory("old")
		m.input.SetValue("first line")
		m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
		m.Update(keyRunes('x'))
		draft := m.input.Value()
		if draft != "first line\nx" {
			t.Fatalf("newline failed: %q", draft)
		}
		for _, direction := range []rune{tea.KeyUp, tea.KeyDown} {
			for i := 0; i < 6; i++ {
				m.Update(tea.KeyPressMsg{Code: direction})
			}
			if m.input.Value() != draft || m.input.Height() < 2 {
				t.Fatalf("state %d: arrows replaced multiline draft: %q", state, m.input.Value())
			}
		}
	}
	m := sizeModel(t, testModel())
	m.state = stDone
	m.pushHistory("old")
	m.input.SetValue("single line")
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.input.Value() != "single line" {
		t.Fatal("down erased idle draft")
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

// The bubbles textarea defaults line numbers on: an empty box then
// renders a phantom "1" that looks like content but submits nothing.
func TestInputHidesLineNumbers(t *testing.T) {
	m := testModel()
	if view := m.input.View(); strings.Contains(view, "1") {
		t.Fatalf("empty input renders %q — line gutter leaking?", view)
	}
}

func TestNativeUnderlineCaretTracksComposerAndHidesOnHelp(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	m.input.SetValue("first\nsecond")
	m.input.CursorEnd()
	m.fitInput()
	m.fitBottom()
	v := m.View()
	if v.Cursor == nil || v.Cursor.Shape != tea.CursorUnderline || v.Cursor.Y != m.termH-m.footerRows()-1 {
		t.Fatalf("incorrect native composer cursor: %+v", v.Cursor)
	}
	if v.Cursor.X != 2+len("second") {
		t.Fatalf("caret did not follow text: %+v", v.Cursor)
	}
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyF1})
	if m.View().Cursor != nil {
		t.Fatal("help exposed background caret")
	}
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyF1})
	if m.View().Cursor == nil || m.input.Value() != "first\nsecond" {
		t.Fatal("closing help lost caret or draft")
	}
}

// Breathing room: the last transcript line never touches the divider —
// a blank line sits between content and rule at the bottom.
func TestTranscriptBreathesBeforeDivider(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	for _, b := range renderHistory(compactTestHistory(15)) {
		m.appendBlock(b)
	}
	m.vp.GotoBottom()
	lines := strings.Split(m.View().Content, "\n")
	// The composer rule sits below the transcript breathing row.
	div := 3 + m.vp.Height() + 1
	if div < 1 {
		t.Fatalf("no divider in view:\n%s", m.View().Content)
	}
	if strings.TrimSpace(lines[div-1]) != "" {
		t.Fatalf("line above divider = %q, want blank", lines[div-1])
	}
}

func TestNewlineKey(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	m.input.SetValue("a")
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	mm := updated.(*model)
	if !strings.Contains(mm.input.Value(), "\n") {
		t.Fatalf("ctrl+o must insert newline, got %q", mm.input.Value())
	}
}

func TestInterruptStaysInChat(t *testing.T) {
	m := sizeModel(t, testModel())
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	mm := updated.(*model)
	if mm.quit {
		t.Fatal("esc must interrupt, not quit")
	}
	if mm.state != stStopping {
		t.Fatalf("state = %v, want stopping", mm.state)
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
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	mm := updated.(*model)
	if mm.follow {
		t.Fatal("pgup must unfollow")
	}
	if !strings.Contains(ansi.Strip(mm.View().Content), "Ctrl+End Latest") {
		t.Fatal("unfollowed state must show jump hint")
	}
	updated, _ = mm.Update(tea.KeyPressMsg{Code: tea.KeyEnd, Mod: tea.ModCtrl})
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
	y0 := m.vp.YOffset()
	m.appendBlock(markerBlock("new block while reading"))
	if m.vp.YOffset() != y0 {
		t.Fatal("new block yanked a scrolled-up viewport")
	}
}

func TestQuitNeedsCtrlQ(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	// Typing a q-word never quits, first letter or not.
	for _, r := range "queen" {
		updated, _ := m.Update(keyRunes(r))
		mm := updated.(*model)
		if mm.quit {
			t.Fatalf("typing %q quit", r)
		}
		m = mm
	}
	if m.input.Value() != "queen" {
		t.Fatalf("input = %q, want the typed word", m.input.Value())
	}
	// A lone q on an empty box is still just typing.
	m.input.SetValue("")
	updated, _ := m.Update(keyRunes('q'))
	if mm := updated.(*model); mm.quit || mm.input.Value() != "q" {
		t.Fatal("q must type, never quit")
	}
	// Quit lives on ctrl+q, with or without a draft.
	updated, _ = m.Update(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl})
	if mm := updated.(*model); !mm.quit {
		t.Fatal("ctrl+q must quit")
	}
}

func TestDialogQDoesNotClose(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	m.openDialog("help", renderHelp())
	updated, _ := m.Update(keyRunes('q'))
	if mm := updated.(*model); mm.dialog == nil {
		t.Fatal("q must not close the dialog")
	}
}

func TestAskCtrlQStopsBeforeQuit(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stAsk
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl})
	if mm := updated.(*model); mm.quit || mm.state != stStopping {
		t.Fatal("ctrl+q must stop the active ask before quitting")
	}
}

func TestQDraftsDuringRun(t *testing.T) {
	m := sizeModel(t, testModel())
	m.input.Focus()
	updated, _ := m.Update(keyRunes('q'))
	mm := updated.(*model)
	if mm.quit || mm.state != stRunning || mm.input.Value() != "q" {
		t.Fatal("q must type a draft during inference")
	}
}

func TestScrollRoutedInEveryState(t *testing.T) {
	for _, state := range []runState{stRunning, stDone} {
		m := sizeModel(t, testModel())
		for i := 0; i < 40; i++ {
			m.appendBlock(markerBlock("block"))
		}
		if !m.follow {
			t.Fatalf("state %v: follow expected while at bottom", state)
		}
		m.state = state
		updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
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
	wheel := tea.MouseWheelMsg{Button: tea.MouseWheelUp}
	updated, _ := m.Update(wheel)
	if mm := updated.(*model); mm.follow {
		t.Fatal("wheel-up in done state must unfollow")
	}
}

func TestSpinnerBelongsToStatusAndBrandStaysStatic(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stRunning
	brand := m.brand()
	first := m.spinner()
	if !strings.Contains(m.identityLine(), "⠋") {
		t.Fatal("spinner missing from run status")
	}
	if strings.Contains(m.headerLine(), "⠋") {
		t.Fatal("spinner placed in title")
	}
	m.Update(tickMsg(time.Now()))
	if m.spinner() == first || m.brand() != brand {
		t.Fatal("spinner did not advance or brand changed")
	}
	for _, state := range []runState{stAsk, stPermission, stDone} {
		m.state = state
		if m.spinner() != "" || m.brand() != brand {
			t.Fatal("paused or idle state animates")
		}
	}
	m.state = stStopping
	if m.spinner() == "" {
		t.Fatal("stopping has no activity indicator")
	}
	m.state = stRunning
	width := lipgloss.Width(m.identityLine())
	m.state = stDone
	if lipgloss.Width(m.identityLine()) != width {
		t.Fatal("activity moved the header layout")
	}
}

func TestEmptyStart_SubmitBegins(t *testing.T) {
	m := sizeModel(t, testModel())
	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m.newSession = func(task, stateFile string, images []string) (*api.Session, error) {
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
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
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
	if mm.blocks[1].text != "hello?" {
		t.Fatalf("echo text = %q, want verbatim task", mm.blocks[1].text)
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
	want := []role{roleThink, roleAnswer, roleTool}
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
	collapsed := renderBlock(thinkBlock(long), st, false, 80)
	if !strings.Contains(collapsed, "[THINKING]") {
		t.Fatalf("collapsed thinking needs its header: %q", collapsed)
	}
	if !strings.Contains(collapsed, "chars]") {
		t.Fatalf("collapsed thinking shows size only here: %q", collapsed)
	}
	if strings.Contains(collapsed, "Second line of musing") {
		t.Fatalf("collapsed thinking leaks body: %q", collapsed)
	}
	expanded := renderBlock(thinkBlock(long), st, true, 80)
	// Wrapping splits phrases across rail-indented lines: normalize
	// all whitespace before matching.
	flat := strings.Join(strings.Fields(ansi.Strip(expanded)), " ")
	if !strings.Contains(flat, "Second line of musing") {
		t.Fatalf("expanded thinking must show all: %q", expanded)
	}
	if strings.Contains(expanded, "chars]") {
		t.Fatalf("expanded thinking must not repeat the size: %q", expanded)
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
	if !m.compact {
		t.Fatal("history starts compact")
	}
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	if mm := updated.(*model); mm.compact {
		t.Fatal("ctrl+g must open full view")
	} else {
		updated, _ = mm.Update(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
		if mm := updated.(*model); !mm.compact {
			t.Fatal("ctrl+g again must compact")
		}
	}
	// A plain "t" types, never toggles.
	mm := sizeModel(t, testModel())
	mm.state = stDone
	mm.input.SetValue("ed")
	updated, _ = mm.Update(tea.KeyPressMsg{Text: string([]rune{'t'})})
	if mm := updated.(*model); !mm.compact || mm.input.Value() != "edt" {
		t.Fatalf("t must type, not toggle: compact=%v value=%q", mm.compact, mm.input.Value())
	}
}

// Tool card headers collapse multi-line model args to one line
// (the open card's pending rail below is structure, not args).
func TestToolCardHeader_SingleLine(t *testing.T) {
	st := defaultStyles()
	out := renderBlock(toolCardBlock("c1", "write_file {\n  \"path\": \"a.go\"\n}"), st, true, 80)
	lines := strings.Split(out, "\n")
	if len(lines) != 3 || !strings.Contains(lines[1], `{ "path": "a.go" }`) {
		t.Fatalf("card call must hold collapsed args: %q", out)
	}
}

func TestHelpMentionsThinkKey(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	m.input.SetValue("/help")
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm := updated.(*model)
	mm.dialog.page = 1
	if !strings.Contains(mm.dialogView(), thinkToggleHint) {
		t.Fatal("help must document the think key")
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

func keyRunes(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Text: string([]rune{r})}
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
	updated, _ = mm.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	mm = updated.(*model)
	if mm.gstage != gsPermit || !mm.hub.HasPending() {
		t.Fatal("esc must return to permit, still pending")
	}
	// Enter on confirm answers "a" and tears the overlay down.
	updated, _ = mm.Update(keyRunes('a'))
	mm = updated.(*model)
	updated, _ = mm.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
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
	updated, _ = mm.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
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
	updated, _ = mm.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if rep := awaitReply(t, answered); rep.Answer != "n" {
		t.Fatalf("reply = %+v, want plain deny", rep)
	}
}

func TestGateHook_AuditsDecisions(t *testing.T) {
	m := sizeModel(t, testModel())
	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	hook := audit.Hook(path, "tui", m.gateHook(m.hub, context.Background(), ws))
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = hook("read_file", "a.txt", json.RawMessage(`{}`))
	}()
	waitPending(t, m.hub)
	if err := m.hub.Respond("y"); err != nil {
		t.Fatal(err)
	}
	<-done
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"front":"tui"`) || !strings.Contains(string(data), `"effect":"allow"`) {
		t.Fatalf("audit record missing: %s", data)
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
	out := renderBlock(block{role: roleUser, text: "❯ hi", at: at}, st, false, 80)
	if !strings.Contains(out, "[14:22]") {
		t.Fatalf("user block missing timestamp: %q", out)
	}
	// Unstamped blocks (zero time) still carry speaker identity, just
	// no timestamp: headers are structure, not chrome.
	plain := renderBlock(block{role: roleAnswer, text: "hi"}, st, false, 80)
	if !strings.Contains(plain, "[RESPONSE]") {
		t.Fatalf("answer missing speaker header: %q", plain)
	}
	if strings.Contains(plain, "[14:22]") {
		t.Fatalf("zero-time block must not stamp: %q", plain)
	}
}

func TestInitialTaskEchoedAsUser(t *testing.T) {
	m := sizeModel(t, testModel())
	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m.newSession = func(task, stateFile string, images []string) (*api.Session, error) {
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
	if err := m.startInitial("  hi  ", nil); err != nil {
		t.Fatal(err)
	}
	if m.state != stRunning {
		t.Fatalf("state = %v, want running", m.state)
	}
	if len(m.blocks) != 1 || m.blocks[0].role != roleUser {
		t.Fatalf("opening task must echo as user block: %+v", m.blocks)
	}
	if m.blocks[0].text != "hi" {
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
	api.EmitStep(em, "run", 1, llm.Message{Content: text, Reasoning: reason}, false, 0)
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
	if mm.blocks[0].role != roleThink || mm.blocks[0].text != reason {
		t.Fatal("reasoning cut or misroled by the funnel")
	}
	if mm.blocks[1].role != roleAnswer || mm.blocks[1].text != text {
		t.Fatal("answer cut or misroled by the funnel")
	}
}

func TestReflow(t *testing.T) {
	for _, tc := range []struct {
		in    string
		width int
		want  []string
	}{
		{"aaa bbb ccc ddd eee", 8, []string{"aaa bbb", "ccc ddd", "eee"}},
		{"  indented line here", 10, []string{"  indented", "line here"}},
		{"abcdefghij", 8, []string{"abcdefgh", "ij"}},
		{"a\tb", 80, []string{"a    b"}},
		{"", 80, []string{""}},
		{"short", 2, []string{"short"}},
	} {
		got := reflow(tc.in, tc.width)
		if len(got) != len(tc.want) {
			t.Fatalf("reflow(%q, %d) = %q, want %q", tc.in, tc.width, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("reflow(%q, %d) = %q, want %q", tc.in, tc.width, got, tc.want)
			}
		}
	}
}

func TestRenderBlockWrapsToWidth(t *testing.T) {
	st := defaultStyles()
	text := "lorem ipsum dolor sit amet consectetur adipiscing elit sed do"
	out := renderBlock(answerBlock(text), st, false, 20)
	for _, line := range strings.Split(out, "\n") {
		if n := lipgloss.Width(line); n > 20 {
			t.Fatalf("line %d cols past 20: %q", n, line)
		}
	}
	// Wrapped continuations sit on the 6-wide speaker rail, under the
	// tag bracket — aligned, not new.
	out = renderBlock(userBlock("aaa bbb ccc ddd eee fff"), st, false, 20)
	lines := strings.Split(ansi.Strip(out), "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[0], "YOU") {
		t.Fatalf("speaker header missing: %q", out)
	}
	for _, ln := range lines[1:] {
		if strings.TrimSpace(ln) != "" && !strings.HasPrefix(ln, "      ") {
			t.Fatalf("continuation off rail: %q", out)
		}
	}
}

func TestPreReadyBlocksRender(t *testing.T) {
	m := testModel() // never sized: not ready
	m.appendBlock(markerBlock("early"))
	mm := sizeModel(t, m)
	if view := mm.View().Content; !strings.Contains(view, "early") {
		t.Fatalf("pre-ready block missing: %q", view)
	}
}

func TestStatusPostsToHistory(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	m.limit = 131072
	m.tokens = 12400
	m.input.SetValue("/status")
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm := updated.(*model)
	if mm.dialog != nil {
		t.Fatal("status must post to history, not a modal")
	}
	view := m.vp.View()
	for _, want := range []string{"backend", "workspace", "context", "ctx 12.4k / 131.1k (9%)", "permissions"} {
		if !strings.Contains(view, want) {
			t.Fatalf("status missing %q", want)
		}
	}
}

func TestKvRowsAlignsValues(t *testing.T) {
	rows := kvRows([]string{"a: 1", "longer: 2", "no-colon"})
	if rows[2] != "no-colon" {
		t.Fatalf("colonless row must pass through: %q", rows[2])
	}
	i1, i2 := strings.Index(rows[0], "1"), strings.Index(rows[1], "2")
	if i1 != i2 {
		t.Fatalf("values misaligned: %q vs %q", rows[0], rows[1])
	}
}

func TestStatusLinesPlainLanguage(t *testing.T) {
	m := sizeModel(t, testModel())
	m.backendKind, m.modelName = "llama", "local"
	out := strings.Join(m.statusLines(), "\n")
	for _, banned := range []string{"staged", "always-memory", "pairs", "budget default chars"} {
		if strings.Contains(out, banned) {
			t.Fatalf("status keeps harness jargon %q:\n%s", banned, out)
		}
	}
	for _, want := range []string{"details", "Ctrl+G", "permissions", "Y allow once", "always allowed", "none"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status missing %q:\n%s", want, out)
		}
	}
}

func TestWithoutPrintHooks_Silent(t *testing.T) {
	env := withoutPrintHooks(roles.Env{})
	// Callable with dummy args: silence by construction, never a
	// nil dereference whatever the loop passes.
	env.OnStep("l", 1, llm.Message{Content: "x"})
	env.OnToolResult("c", "r")
	env.OnResult(agent.ToolResult{})
	env.OnUsage(1, llm.Usage{})
	env.OnDelta("z")
}

func TestDialogFitsViewport(t *testing.T) {
	m := sizeModel(t, testModel())
	m.openDialog("status", m.statusLines())
	m.vp.SetWidth(26)
	m.vp.SetHeight(8)
	out := m.dialogView()
	lines := strings.Split(out, "\n")
	if len(lines) > 8 {
		t.Fatalf("%d dialog lines past height 8", len(lines))
	}
	for _, ln := range lines {
		if n := lipgloss.Width(ln); n > 26 {
			t.Fatalf("dialog line %d cols past 26: %q", n, ln)
		}
	}
}

func TestStartRunClosesDialog(t *testing.T) {
	m := sizeModel(t, testModel())
	m.send = func(tea.Msg) {}
	m.ctx = context.Background()
	m.openDialog("help", renderHelp())
	if len(m.overlays) != 1 {
		t.Fatal("setup: dialog must push")
	}
	m.startRun(func(ctx context.Context) (string, error) { return "done", nil })
	if m.dialog != nil || len(m.overlays) != 0 {
		t.Fatal("run start must close the dialog")
	}
}

func TestTruncateRuneSafe(t *testing.T) {
	if got := truncate(strings.Repeat("—", 10), 8); got != strings.Repeat("—", 8)+"…" {
		t.Fatalf("truncate = %q", got)
	}
	if truncate("abc", 8) != "abc" {
		t.Fatal("short text altered")
	}
}

func TestDividerExactWidth(t *testing.T) {
	m := sizeModel(t, testModel())
	m.appendBlock(userBlock("hi"))
	m.appendBlock(answerBlock("there"))
	line := m.dividerLine()
	if n := lipgloss.Width(line); n != m.vp.Width() {
		t.Fatalf("divider %d cols, want viewport %d", n, m.vp.Width())
	}
}

func TestBottomZoneHeights(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if mm := updated.(*model); mm.vp.Height() != 14 {
		t.Fatalf("done viewport height = %d, want 14 (24 - header - identity - rule - status - rule - breathing row - input - footer separator - actions)", mm.vp.Height())
	}
	mm := updated.(*model)
	mm.state = stRunning
	updated, _ = mm.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if mm := updated.(*model); mm.vp.Height() != 14 {
		t.Fatalf("running viewport height = %d, want 14", mm.vp.Height())
	}
	// Composing keeps the same budget: the hint stays put, so the
	// layout never shifts while typing (user call: always visible).
	mm.input.SetValue("typing")
	mm.state = stDone
	updated, _ = mm.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if mm = updated.(*model); mm.vp.Height() != 14 {
		t.Fatalf("composing viewport height = %d, want 14", mm.vp.Height())
	}
	// The frame uses every row, including the footer at the bottom edge.
	if n := len(strings.Split(mm.View().Content, "\n")); n != 24 {
		t.Fatalf("view is %d lines, want 24", n)
	}
}

// Keys live only in /help now that the status row carries state
// instead of a manual: every essential binding must be listed there.
func TestHintAlwaysShown(t *testing.T) {
	for _, want := range []string{"Ctrl+Q", "Ctrl+G", "Enter", "Alt+Up / Alt+Down", "Esc"} {
		found := false
		for _, s := range helpSections() {
			for _, r := range s.rows {
				if strings.Contains(r[0], want) {
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("help teaches no %q", want)
		}
	}
}

func TestHelpSections(t *testing.T) {
	st := defaultStyles()
	out := renderBlock(markerBlock(strings.Join(renderHelp(), "\n")), st, false, 80)
	for _, want := range []string{"Input", "Navigate", "Run", "Commands"} {
		if !strings.Contains(out, want) {
			t.Fatalf("help missing %q", want)
		}
	}
	if strings.Contains(out, "thread") {
		t.Fatalf("thread strip removed everywhere: %q", out)
	}
	// Two columns: every command description starts at the same
	// absolute column (keys padded to the section max).
	cmds := map[string]string{
		"/quit": "Quit", "/help": "Open this help", "/new [task]": "Start a fresh task; omit task for an empty session",
		"/status": "Show current run facts", "/retry": "Retry the last failed turn", "/sessions": "Browse saved sessions",
		"/compact": "Shrink this session's history",
	}
	col, found := -1, 0
	for _, ln := range renderHelp() {
		rest := strings.TrimPrefix(ln, "  ")
		for k, d := range cmds {
			if !strings.HasPrefix(rest, k+" ") {
				continue
			}
			found++
			if i := strings.Index(ln, d); col < 0 {
				col = i
			} else if i != col {
				t.Fatalf("command columns drift: %q", ln)
			}
		}
	}
	if found != len(cmds) {
		t.Fatalf("only %d command rows found", found)
	}
}

func TestStampsRightAligned(t *testing.T) {
	st := defaultStyles()
	b := answerBlock("short")
	b.at = time.Date(2026, 10, 3, 14, 22, 0, 0, time.Local)
	out := renderBlock(b, st, false, 40)
	lines := strings.Split(out, "\n")
	if len(lines) != 2 {
		t.Fatalf("speaker block is header + body: %q", out)
	}
	if n := lipgloss.Width(lines[1]); n != 40 {
		t.Fatalf("stamp line %d cols, want full 40", n)
	}
	if !strings.Contains(lines[1], "[14:22]") {
		t.Fatalf("stamp missing: %q", lines[1])
	}
}

func TestCleanTextStripsCR(t *testing.T) {
	if got := cleanText("a\r\nb\rc"); got != "a\nb\nc" {
		t.Fatalf("cleanText = %q", got)
	}
}

func TestCRLFContentStaysInWidth(t *testing.T) {
	st := defaultStyles()
	// CRLF tool output (read_file on a Windows file) must render with
	// no stray CR and no line past the width: a \r mid-line returns
	// the cursor to column 0, so stamp padding overwrites the head
	// and the alt-screen frame desyncs (ghost text over stale rows).
	b := toolCardBlock("c1", "read_file a.md")
	b.result = "FILE\npath: a.md\n----\n# Title\r\n\r\nbody line\r\n"
	b.open = false
	for _, line := range strings.Split(renderBlock(b, st, true, 80), "\n") {
		if strings.Contains(line, "\r") {
			t.Fatalf("CR survived render: %q", line)
		}
		if n := lipgloss.Width(line); n > 80 {
			t.Fatalf("line %d cols past 80: %q", n, line)
		}
	}
	// Stamped CRLF answer: the stamp line must keep its head text.
	out := renderBlock(answerBlock("integrity.\r\nsecond line"), st, false, 40)
	if strings.Contains(out, "\r") {
		t.Fatalf("CR survived stamped render: %q", out)
	}
	lines := strings.Split(out, "\n")
	if len(lines) < 2 || !strings.Contains(lines[1], "integrity.") || !strings.Contains(lines[1], "[") {
		t.Fatalf("stamp line lost its head: %q", out)
	}
}

func TestBareSkipsGutter(t *testing.T) {
	st := defaultStyles()
	b := markerBlock("note")
	w0 := lipgloss.Width(renderBlock(b, st, false, 80))
	b.bare = true
	w1 := lipgloss.Width(renderBlock(b, st, false, 80))
	if w0-w1 != len(speakerRail) {
		t.Fatalf("bare must drop the system label column: %d vs %d", w0, w1)
	}
}

func TestFailedRunKeepsErrorBlock(t *testing.T) {
	m := sizeModel(t, testModel())
	updated, _ := m.Update(doneMsg{err: errors.New("boom")})
	mm := updated.(*model)
	if mm.runErr == nil {
		t.Fatal("runErr must survive a failed run")
	}
	found := false
	for _, b := range mm.blocks {
		if b.role == roleError && strings.Contains(b.text, "boom") {
			found = true
		}
	}
	if !found {
		t.Fatalf("error missing from transcript: %+v", mm.blocks)
	}
}

func TestRetryRerunsLastTurn(t *testing.T) {
	m := sizeModel(t, testModel())
	m.send = func(tea.Msg) {}
	m.ctx = context.Background()
	m.retryRun = func(ctx context.Context) (string, error) { return "recovered", nil }
	m.runErr = errors.New("boom")
	m.state = stDone
	m.input.SetValue("/retry")
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm := updated.(*model)
	if mm.state != stRunning {
		t.Fatalf("state = %v, want running", mm.state)
	}
	updated, _ = mm.Update(doneMsg{answer: "recovered"})
	mm = updated.(*model)
	if mm.runErr != nil || mm.answer != "recovered" {
		t.Fatalf("runErr = %v answer = %q", mm.runErr, mm.answer)
	}
}

func TestRetryRefusesWhenOk(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	m.input.SetValue("/retry")
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm := updated.(*model)
	if mm.state != stDone {
		t.Fatalf("state = %v, want done", mm.state)
	}
	found := false
	for _, b := range mm.blocks {
		if strings.Contains(b.text, "nothing to retry") {
			found = true
		}
	}
	if !found {
		t.Fatalf("refusal missing: %+v", mm.blocks)
	}
}

func TestTurnSeparators(t *testing.T) {
	m := sizeModel(t, testModel())
	m.appendBlock(answerBlock("first"))
	m.appendBlock(userBlock("❯ second"))
	view := ansi.Strip(m.vp.View())
	if strings.Contains(view, "─") || !regexp.MustCompile(`\n *\n *\nYOU`).MatchString(view) {
		t.Fatalf("turn should use two blank rows instead of a pane rule: %q", view)
	}
	// Marker-then-echo shares one turn: no extra turn spacing.
	m2 := sizeModel(t, testModel())
	nb := markerBlock("— new task —")
	nb.breakBefore = true
	m2.appendBlock(nb)
	m2.appendBlock(userBlock("❯ go"))
	if n := strings.Count(m2.vp.View(), "─"); n != 0 {
		t.Fatalf("opener pair must not add rules, got %d", n)
	}
}

func TestToolCardAttachesResult(t *testing.T) {
	m := sizeModel(t, testModel())
	updated, _ := m.Update(eventMsg(api.Event{
		Name: "step",
		Fields: map[string]any{
			"tool_calls": []any{map[string]any{"id": "c1", "name": "read_file", "args": "{}"}},
		},
	}))
	mm := updated.(*model)
	if len(mm.blocks) != 1 || !mm.blocks[0].open {
		t.Fatalf("call must open a card: %+v", mm.blocks)
	}
	if view := mm.vp.View(); !strings.Contains(view, "read_file") || !strings.Contains(view, "…") {
		t.Fatalf("open card missing call/pending: %q", view)
	}
	updated, _ = mm.Update(eventMsg(api.Event{
		Name:   "tool_result",
		Fields: map[string]any{"call_id": "c1", "text": "ok"},
	}))
	mm = updated.(*model)
	if len(mm.blocks) != 1 || mm.blocks[0].open {
		t.Fatalf("result must land on the card: %+v", mm.blocks)
	}
	if view := mm.vp.View(); !strings.Contains(view, "ok") || !strings.Contains(view, "✓") {
		t.Fatalf("closed card missing result/check: %q", view)
	}
	// Unknown IDs never corrupt a card: standalone block instead.
	updated, _ = mm.Update(eventMsg(api.Event{
		Name:   "tool_result",
		Fields: map[string]any{"call_id": "zz", "text": "stray"},
	}))
	if mm := updated.(*model); len(mm.blocks) != 2 || mm.blocks[1].role != roleResult {
		t.Fatalf("stray result must stand alone: %+v", mm.blocks)
	}
}

func TestToolResultCollapsesLongOutput(t *testing.T) {
	st := defaultStyles()
	text := strings.TrimRight(strings.Repeat("abcdef\n", 25), "\n")
	mk := func() block {
		return block{role: roleTool, text: "run x", callID: "c1", result: text}
	}
	compact := renderBlock(mk(), st, false, 80)
	if n := strings.Count(compact, "abcdef"); n != resultPreviewLines {
		t.Fatalf("compact shows %d lines, want %d", n, resultPreviewLines)
	}
	if !strings.Contains(compact, "(15 more lines)") {
		t.Fatalf("compact must count the rest: %q", compact)
	}
	full := renderBlock(mk(), st, true, 80)
	if n := strings.Count(full, "abcdef"); n != 25 {
		t.Fatalf("full view shows %d lines, want 25", n)
	}
	if strings.Contains(full, "more lines") {
		t.Fatalf("full view must not mark: %q", full)
	}
}

func TestToolCardFailedGlyph(t *testing.T) {
	m := sizeModel(t, testModel())
	updated, _ := m.Update(eventMsg(api.Event{
		Name: "step",
		Fields: map[string]any{
			"tool_calls": []any{map[string]any{"id": "c2", "name": "run_shell", "args": "{}"}},
		},
	}))
	mm := updated.(*model)
	updated, _ = mm.Update(eventMsg(api.Event{
		Name:   "tool_result",
		Fields: map[string]any{"call_id": "c2", "text": "error: boom"},
	}))
	mm = updated.(*model)
	if !mm.blocks[0].failed {
		t.Fatal("error-prefixed result must flag the card")
	}
	if view := mm.vp.View(); !strings.Contains(view, "✕") {
		t.Fatalf("failed card missing cross: %q", view)
	}
}

func TestFindingRendersWarning(t *testing.T) {
	m := sizeModel(t, testModel())
	updated, _ := m.Update(eventMsg(api.Event{
		Name: "finding",
		Fields: map[string]any{
			"scope": "per-edit", "rule": "gofmt", "path": "a.go",
			"line": float64(3), "summary": "not gofmt-clean",
		},
	}))
	mm := updated.(*model)
	if len(mm.blocks) != 1 || mm.blocks[0].role != roleWarning {
		t.Fatalf("finding must be a warning block: %+v", mm.blocks)
	}
	if view := mm.vp.View(); !strings.Contains(view, "WARN") || !strings.Contains(view, "a.go:3") {
		t.Fatalf("warning missing rail/content: %q", view)
	}
	// Rule-less events append nothing, never a blank warning.
	updated, _ = mm.Update(eventMsg(api.Event{Name: "finding", Fields: map[string]any{}}))
	if mm := updated.(*model); len(mm.blocks) != 1 {
		t.Fatal("malformed finding must not append")
	}
}

func TestNudgeRendersMarker(t *testing.T) {
	m := sizeModel(t, testModel())
	updated, _ := m.Update(eventMsg(api.Event{
		Name:   "nudge",
		Fields: map[string]any{"kind": "verify", "text": "[harness] check your work"},
	}))
	mm := updated.(*model)
	if len(mm.blocks) != 1 || mm.blocks[0].role != roleMarker {
		t.Fatalf("nudge must be a marker block: %+v", mm.blocks)
	}
	if view := mm.vp.View(); !strings.Contains(view, "[harness]") {
		t.Fatalf("provenance mark missing: %q", view)
	}
	// Empty nudges append nothing.
	updated, _ = mm.Update(eventMsg(api.Event{Name: "nudge", Fields: map[string]any{}}))
	if mm := updated.(*model); len(mm.blocks) != 1 {
		t.Fatal("empty nudge must not append")
	}
}

func TestLiveAccumulatesAndStepReplaces(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stRunning
	updated, _ := m.Update(eventMsg(api.Event{Name: "delta", Fields: map[string]any{"text": "hel"}}))
	mm := updated.(*model)
	if view := mm.vp.View(); !strings.Contains(view, "hel") {
		t.Fatalf("first delta must paint: %q", view)
	}
	updated, _ = mm.Update(eventMsg(api.Event{Name: "delta", Fields: map[string]any{"text": "lo"}}))
	mm = updated.(*model)
	if mm.live != "hello" {
		t.Fatalf("live = %q", mm.live)
	}
	// Step replaces without duplication.
	updated, _ = mm.Update(eventMsg(api.Event{Name: "step", Fields: map[string]any{"text": "hello"}}))
	mm = updated.(*model)
	if mm.live != "" {
		t.Fatal("step must clear the live buffer")
	}
	if n := strings.Count(mm.vp.View(), "hello"); n != 1 {
		t.Fatalf("hello appears %d times, want exactly 1", n)
	}
}

func TestLiveAdoptedWhenStepOmits(t *testing.T) {
	m := sizeModel(t, testModel())
	updated, _ := m.Update(eventMsg(api.Event{Name: "delta", Fields: map[string]any{"text": "visible"}}))
	mm := updated.(*model)
	updated, _ = mm.Update(eventMsg(api.Event{Name: "step", Fields: map[string]any{}}))
	mm = updated.(*model)
	found := false
	for _, b := range mm.blocks {
		if b.role == roleAnswer && strings.Contains(b.text, "visible") {
			found = true
		}
	}
	if !found {
		t.Fatalf("omitted step must adopt the buffer: %+v", mm.blocks)
	}
}

func TestLiveDiscardedOnDone(t *testing.T) {
	m := sizeModel(t, testModel())
	updated, _ := m.Update(eventMsg(api.Event{Name: "delta", Fields: map[string]any{"text": "partial"}}))
	mm := updated.(*model)
	updated, _ = mm.Update(doneMsg{answer: "done"})
	if mm := updated.(*model); mm.live != "" {
		t.Fatal("done must discard partial live text")
	}
}

func TestLiveUnfollowedStability(t *testing.T) {
	m := sizeModel(t, testModel())
	for i := 0; i < 40; i++ {
		m.appendBlock(markerBlock("filler"))
	}
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	mm := updated.(*model)
	if mm.follow {
		t.Fatal("setup: pgup must unfollow")
	}
	y0 := mm.vp.YOffset()
	for _, chunk := range []string{"a", "b", "c", "d", "e"} {
		updated, _ = mm.Update(eventMsg(api.Event{Name: "delta", Fields: map[string]any{"text": chunk}}))
		mm = updated.(*model)
	}
	if mm.vp.YOffset() != y0 {
		t.Fatalf("live growth below moved offset %d -> %d", y0, mm.vp.YOffset())
	}
	if mm.live != "abcde" {
		t.Fatalf("live = %q", mm.live)
	}
}

func TestLiveCapped(t *testing.T) {
	m := sizeModel(t, testModel())
	big := strings.Repeat("z", liveMaxRunes+10)
	updated, _ := m.Update(eventMsg(api.Event{Name: "delta", Fields: map[string]any{"text": big}}))
	mm := updated.(*model)
	if n := len([]rune(mm.live)); n <= liveMaxRunes {
		t.Fatalf("live = %d runes, want over cap with marker", n)
	}
	if !mm.liveCut || !strings.Contains(mm.live, "live truncated") {
		t.Fatal("cap must mark once")
	}
	before := mm.live
	updated, _ = mm.Update(eventMsg(api.Event{Name: "delta", Fields: map[string]any{"text": "more"}}))
	if mm := updated.(*model); mm.live != before {
		t.Fatal("capped buffer must drop further chunks")
	}
}

func TestHarnessReplyRendersNormally(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	updated, _ := m.Update(eventMsg(api.Event{
		Name: "step", Fields: map[string]any{"text": "real answer"},
	}))
	mm := updated.(*model)
	updated, _ = mm.Update(eventMsg(api.Event{
		Name: "nudge", Fields: map[string]any{"kind": "verify", "text": "[harness] check"},
	}))
	mm = updated.(*model)
	updated, _ = mm.Update(eventMsg(api.Event{
		Name: "step", Fields: map[string]any{"text": "noted", "harness_reply": true},
	}))
	mm = updated.(*model)
	if len(mm.blocks) != 3 {
		t.Fatalf("blocks = %d", len(mm.blocks))
	}
	// No hiding anymore: nudges and harness replies render like
	// everything else (hiding model-visible content misleads).
	view := mm.vp.View()
	for _, want := range []string{"real answer", "[harness]", "noted"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q: %q", want, view)
		}
	}
}

func TestThinkExpandsGlobally(t *testing.T) {
	m := sizeModel(t, testModel())
	m.appendBlock(thinkBlock("old deliberation " + strings.Repeat("x", 200)))
	m.appendBlock(thinkBlock("new deliberation"))
	m.compact = false
	m.refreshContent()
	view := m.vp.View()
	// Reflow wraps long runs: count runes, not substrings.
	if n := strings.Count(view, "x"); n < 200 {
		t.Fatalf("global expand opened %d x-runes, want 200", n)
	}
	if n := strings.Count(view, "[THINKING]"); n != 2 {
		t.Fatalf("both thinks must label expanded: %d headers", n)
	}
}

func TestToggleHoldsPosition(t *testing.T) {
	// Growth below the viewport: offset untouched.
	m := sizeModel(t, testModel())
	for i := 0; i < 40; i++ {
		m.appendBlock(markerBlock("filler"))
	}
	m.appendBlock(thinkBlock("reasoning " + strings.Repeat("y", 300)))
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	mm := updated.(*model)
	if mm.follow {
		t.Fatal("setup: pgup must unfollow")
	}
	y0 := mm.vp.YOffset()
	first := strings.Split(mm.vp.View(), "\n")[0]
	updated, _ = mm.Update(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	mm = updated.(*model)
	if mm.vp.YOffset() != y0 {
		t.Fatalf("growth below moved offset %d -> %d", y0, mm.vp.YOffset())
	}
	if got := strings.Split(mm.vp.View(), "\n")[0]; got != first {
		t.Fatal("growth below changed the first line")
	}
	// Growth above: first visible line stays first.
	m2 := sizeModel(t, testModel())
	m2.appendBlock(thinkBlock("reasoning " + strings.Repeat("z", 300)))
	for i := 0; i < 40; i++ {
		m2.appendBlock(markerBlock("filler"))
	}
	updated, _ = m2.Update(tea.KeyPressMsg{Code: tea.KeyEnd, Mod: tea.ModCtrl})
	mm2 := updated.(*model)
	updated, _ = mm2.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	mm2 = updated.(*model)
	if mm2.follow {
		t.Fatal("setup: pgup must unfollow")
	}
	before := mm2.vp.View()
	first2 := strings.Split(before, "\n")[0]
	updated, _ = mm2.Update(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	mm2 = updated.(*model)
	if got := strings.Split(mm2.vp.View(), "\n")[0]; got != first2 {
		t.Fatalf("growth above moved first line:\nwas  %q\nnow  %q", first2, got)
	}
	// Collapse back: byte-identical view returns.
	updated, _ = mm2.Update(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	if got := updated.(*model).vp.View(); got != before {
		t.Fatal("collapse must restore the exact view")
	}
}

func TestStampsPolicy(t *testing.T) {
	st := defaultStyles()
	stampPattern := regexp.MustCompile(`\[[0-2][0-9]:[0-5][0-9]\]`)
	for _, b := range []block{
		toolCardBlock("c1", "run x"),
		resultBlock("out"),
		markerBlock("— done —"),
		missionBlock("m"),
	} {
		// Speaker/tool headers carry structural [BRACKETS] by design;
		// the policy is about timestamps: chrome never shows one.
		if out := renderBlock(b, st, false, 80); stampPattern.MatchString(out) {
			t.Fatalf("chrome must not stamp: %q", out)
		}
	}
	out := renderBlock(userBlock("hi"), st, false, 80)
	if !stampPattern.MatchString(out) {
		t.Fatalf("user blocks must stamp: %q", out)
	}
}

func TestGateEventForwardsFields(t *testing.T) {
	ev := gateEvent("awaiting_input", map[string]any{
		"kind": "permission", "prompt": "allow?",
		"tool": "run_shell", "resource": "go test",
	})
	if ev.Fields["tool"] != "run_shell" || ev.Fields["resource"] != "go test" || ev.Fields["prompt"] != "allow?" {
		t.Fatalf("fields = %v", ev.Fields)
	}
}

func TestStatusLineShowsStepBudget(t *testing.T) {
	m := sizeModel(t, testModel())
	m.steps = 7
	m.maxSteps = 40
	if line := m.runFacts(); !strings.Contains(line, "step 7/40") {
		t.Fatalf("status line missing step budget: %q", line)
	}
}

func TestEnvMaxSteps_DefaultAndOverride(t *testing.T) {
	if got := envMaxSteps(roles.Env{}); got != agent.DefaultMaxSteps {
		t.Fatalf("envMaxSteps(zero) = %d, want default %d", got, agent.DefaultMaxSteps)
	}
	if got := envMaxSteps(roles.Env{MaxSteps: 60}); got != 60 {
		t.Fatalf("envMaxSteps(60) = %d, want 60", got)
	}
}

func TestStepEventUpdatesStepBudget(t *testing.T) {
	m := sizeModel(t, testModel())
	m.maxSteps = 25
	updated, _ := m.Update(eventMsg(api.Event{
		Name:   "step",
		Fields: map[string]any{"max_steps": float64(37), "text": "hi"},
	}))
	if mm := updated.(*model); mm.maxSteps != 37 {
		t.Fatalf("maxSteps = %d, want 37", mm.maxSteps)
	}
}

func TestStatusShowsModel(t *testing.T) {
	m := testModel()
	m.modelName = "qwen"
	view := strings.Join(m.statusLines(), "\n")
	// Values align in one column, so the label and name no longer sit
	// adjacent — assert the name on the model row, not exact spacing.
	found := false
	for _, ln := range strings.Split(view, "\n") {
		if strings.HasPrefix(ln, "model") && strings.Contains(ln, "qwen") {
			found = true
		}
	}
	if !found {
		t.Fatalf("status missing model: %q", view)
	}
}

// ttft (time to first token) shows once streaming starts: the felt
// latency beside elapsed (the total). Fixed clock offsets, no tick
// flakes.
func TestStatusShowsTTFT(t *testing.T) {
	m := testModel()
	m.state = stRunning
	m.started = time.Now().Add(-100 * time.Second)
	if got := m.runFacts(); strings.Contains(got, "ttft") {
		t.Fatalf("status shows ttft before streaming: %q", got)
	}
	m.firstToken = m.started.Add(7 * time.Second)
	if got := m.runFacts(); !strings.Contains(got, "ttft 00:07") {
		t.Fatalf("status missing ttft: %q", got)
	}
	m.state = stDone
	if got := m.runFacts(); strings.Contains(got, "ttft") {
		t.Fatalf("status shows ttft when done: %q", got)
	}
}

// The first streamed chunk stamps firstToken; a new turn clears it.
func TestFirstTokenStampedAndReset(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stRunning
	updated, _ := m.Update(eventMsg(api.Event{Name: "delta", Fields: map[string]any{"text": "hel"}}))
	mm := updated.(*model)
	if mm.firstToken.IsZero() {
		t.Fatal("first delta did not stamp firstToken")
	}
	mm.send = func(tea.Msg) {}
	mm.startRun(func(context.Context) (string, error) { return "", nil })
	if !mm.firstToken.IsZero() {
		t.Fatal("new turn did not reset firstToken")
	}
}

// Brand + clock ride the header: animated letter while running,
// solid amber word at rest.
func TestStatusBrand_RightAligned(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stRunning
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: 24})
	mm := updated.(*model)
	var header string
	for _, ln := range strings.Split(mm.View().Content, "\n") {
		if strings.Contains(ln, "//") {
			header = ln
		}
	}
	if header == "" {
		t.Fatal("header missing brand line")
	}
	if n := lipgloss.Width(header); n != 160 {
		t.Fatalf("header width = %d, want 160 (clock at right edge)", n)
	}
	// Fixed two-line zone: the blocked input stays visible while
	// running (empty transcript here, so "> " can only be the box).
	if !strings.Contains(mm.View().Content, "> ") {
		t.Fatal("running must keep the blocked input box")
	}
	mm.state = stDone
	updated, _ = mm.Update(tea.WindowSizeMsg{Width: 160, Height: 24})
	// Brand owns TARS alone: exactly one occurrence on the row.
	if view := updated.(*model).View().Content; strings.Count(view, "TARS") != 1 {
		t.Fatalf("done header must show one brand: %q", view)
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
	if m.steps != 0 || m.tokens != 42 || m.state != stRunning {
		t.Fatalf("run not reset: steps=%d tokens=%d state=%v", m.steps, m.tokens, m.state)
	}
	if !m.input.Focused() {
		t.Fatal("run start must focus input for drafting")
	}
}

func TestOutcomeUsesObservedChanges(t *testing.T) {
	m := sizeModel(t, testModel())
	m.handleEvent(api.Event{Name: "step", Fields: map[string]any{"tool_calls": []any{map[string]any{"id": "read", "name": "read_file", "args": `{"path":"read-only.go"}`}}}})
	m.handleEvent(api.Event{Name: "outcome", Fields: map[string]any{"outcome": "completed", "files": []any{"changed.go"}}})
	if len(m.filesTouched) != 1 || !m.filesTouched["changed.go"] {
		t.Fatalf("counter must use observed effects: %v", m.filesTouched)
	}
}

func TestToolCardUsesTypedStatus(t *testing.T) {
	m := sizeModel(t, testModel())
	m.appendBlock(toolCardBlock("test", "run_shell"))
	m.handleEvent(api.Event{Name: "tool_result", Fields: map[string]any{"call_id": "test", "text": "ordinary compiler output", "failed": true}})
	if !m.blocks[len(m.blocks)-1].failed {
		t.Fatal("typed failure lost")
	}
	m.appendBlock(toolCardBlock("read", "read_file"))
	m.handleEvent(api.Event{Name: "tool_result", Fields: map[string]any{"call_id": "read", "text": "error: is valid file content", "failed": false}})
	if m.blocks[len(m.blocks)-1].failed {
		t.Fatal("file prose overrode successful status")
	}
}
