package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/keshon/tars/internal/llm"
)

// todoFake is a checklist stub with fixed state: the loop reads it, the
// model never calls it in these scripts.
type todoFake struct {
	done int
	open []string
}

// todoReplay is a checklist stub that stores what it is sent, like the
// real tool: rehydration replays history through Run.
type todoReplay struct {
	items []replayItem
}

type replayItem struct {
	ID    int    `json:"id"`
	State string `json:"state"`
	Text  string `json:"text"`
}

func (*todoReplay) Name() string        { return "todo" }
func (*todoReplay) Description() string { return "stub" }
func (*todoReplay) Mode() ToolMode      { return Concurrent }
func (*todoReplay) Schema() json.RawMessage {
	return json.RawMessage(`{}`)
}
func (t *todoReplay) Run(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Items []replayItem `json:"items"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	t.items = in.Items
	return "ok", nil
}
func (t *todoReplay) Progress() (int, []string) {
	var done int
	var open []string
	for _, it := range t.items {
		if it.State == "done" {
			done++
		} else {
			open = append(open, it.Text)
		}
	}
	return done, open
}

// Every Run/Resume builds a fresh registry, so without rehydration the
// tracker resets every turn: a resumed session with open todos finishes
// without a bounce. Replaying the last todo call's args restores it.
func TestAgent_RehydrateTodo_RestoresGateFromHistory(t *testing.T) {
	history := []llm.Message{
		{Role: llm.RoleUser, Content: "task"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "1", Name: "todo",
			Arguments: json.RawMessage(`{"items":[{"id":1,"state":"done","text":"a"},{"id":2,"state":"pending","text":"b"}]}`)}}},
		{Role: llm.RoleTool, ToolCallID: "1", Content: "todo:\n  [x] 1. a\n  [ ] 2. b\n  (1/2 done)\n"},
	}
	replay := &todoReplay{}
	client := &stubClient{responses: []llm.ChatResponse{
		say("done"), say("done"), say("done"), say("final"),
	}}
	a := New(Config{
		Client: client, Tools: NewRegistry(replay),
		System: "sys", SkipVerify: true,
	})

	out, err := a.Resume(context.Background(), history, "continue")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if out != "final" {
		t.Fatalf("out = %q, want the post-bounce answer", out)
	}
	if done, open := replay.Progress(); done != 1 || len(open) != 1 || open[0] != "b" {
		t.Fatalf("Progress = (%d, %v), want (1, [b])", done, open)
	}
	if n := countInjected(client.lastHistory, "unchecked todo"); n != maxTodoBounces {
		t.Fatalf("bounced %d times, want %d — gate blind to resumed todos", n, maxTodoBounces)
	}
}

func (todoFake) Name() string            { return "todo" }
func (todoFake) Description() string     { return "stub" }
func (todoFake) Mode() ToolMode          { return Concurrent }
func (todoFake) Schema() json.RawMessage { return json.RawMessage(`{}`) }
func (todoFake) Run(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}
func (f todoFake) Progress() (int, []string) { return f.done, f.open }

// A text reply with unchecked todos is bounced, not finished: the latch
// (maxTodoBounces) keeps narration-only loops terminal, then the run
// falls through to normal finish evaluation.
func TestAgent_TodoOpen_BouncesFinishUntilLatch(t *testing.T) {
	client := &stubClient{responses: []llm.ChatResponse{
		say("half done"),
		say("still working"),
		say("almost there"),
		say("final answer"),
	}}
	a := New(Config{
		Client: client, Tools: NewRegistry(todoFake{open: []string{"second item"}}),
		System: "sys", SkipVerify: true,
	})

	out, err := a.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "final answer" {
		t.Fatalf("out = %q", out)
	}
	if n := countInjected(client.lastHistory, "unchecked todo"); n != maxTodoBounces {
		t.Fatalf("bounced %d times, want %d", n, maxTodoBounces)
	}
}

// No list, no gate: an empty checklist (or none) leaves chit-chat alone.
func TestAgent_TodoGate_SkipsWhenListEmpty(t *testing.T) {
	client := &stubClient{responses: []llm.ChatResponse{say("hi")}}
	a := New(Config{
		Client: client, Tools: NewRegistry(todoFake{}),
		System: "sys", SkipVerify: true,
	})

	out, err := a.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "hi" {
		t.Fatalf("out = %q", out)
	}
	if client.calls != 1 {
		t.Fatalf("calls = %d, want 1", client.calls)
	}
	for _, m := range client.lastHistory {
		if m.Role == llm.RoleUser && strings.HasPrefix(m.Content, "[harness] ") {
			t.Fatalf("empty list must not summon a nudge: %q", m.Content)
		}
	}
}

// Live audit, 2026-10-04: a read-only run got the zero-writes paragraph
// and "fixed" it by writing an AUDIT.md nobody requested. The paragraph
// is evidence of failure only where writes were expected or the answer
// claims file effects — the general verify round still fires.
func TestAgent_ZeroWritesFact_OmittedForReadOnlyTask(t *testing.T) {
	var runs int32
	client := &stubClient{responses: []llm.ChatResponse{
		readStep("1", "a.go"),
		say("audit complete, nothing to write"),
		say("confirmed"),
	}}
	a := New(Config{
		Client: client, Tools: NewRegistry(readStub{name: "read_file", runs: &runs}),
		System: "sys",
	})

	out, err := a.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "confirmed" {
		t.Fatalf("out = %q", out)
	}
	for _, m := range client.lastHistory {
		if strings.Contains(m.Content, "0 file-writing") {
			t.Fatalf("read-only task must not see the zero-writes fact: %q", m.Content)
		}
	}
	if n := countInjected(client.lastHistory, "Before you finish"); n != 1 {
		t.Fatalf("general verify round fired %d times, want 1", n)
	}
}

func TestFormatTodoOpen_CapsLongLists(t *testing.T) {
	open := []string{"a", "b", "c", "d", "e", "f", "g"}
	got := formatTodoOpen(open)
	for _, want := range []string{"- a", "- e"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "- f") {
		t.Fatalf("list not capped: %q", got)
	}
	if !strings.Contains(got, "(+2 more)") {
		t.Fatalf("missing overflow count: %q", got)
	}
	if got != strings.TrimSpace(got) {
		t.Fatalf("leading/trailing blank: %q", got)
	}
}
