package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/keshon/tars/internal/llm"
)

// scriptTodo advances a scripted checklist per Run call: states[0] is the
// pre-first-call state, states[n] after n calls (clamped to the last).
// Models phased tool behavior without parsing arguments. Idempotent like
// the real Todo, so byte-identical payloads short-circuit.
type scriptTodo struct {
	mu     sync.Mutex
	calls  int
	states []todoPhase
}

type todoPhase struct {
	done int
	open []string
}

func (*scriptTodo) Name() string            { return "todo" }
func (*scriptTodo) Description() string     { return "stub" }
func (*scriptTodo) Mode() ToolMode          { return Concurrent }
func (*scriptTodo) Schema() json.RawMessage { return json.RawMessage(`{}`) }

// Models the real Todo (idempotent since the ritual fix): identical
// payloads short-circuit instead of re-executing.
func (*scriptTodo) Idempotent() bool { return true }
func (p *scriptTodo) Run(context.Context, json.RawMessage) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return "ok", nil
}
func (p *scriptTodo) Progress() (int, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.states) == 0 {
		return 0, nil
	}
	s := p.states[min(p.calls, len(p.states)-1)]
	return s.done, s.open
}

func todoCall(id, items string) llm.ChatResponse {
	return llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
		{ID: id, Name: "todo", Arguments: json.RawMessage(`{"items":` + items + `}`)}}}}
}

// Creation funds cold-start, completions fund the tail: 2 open (+8) then
// 1 done (+4) against a 25 base. Bounces still fire on the narration
// steps (the open item survives), proving gate and funding independent.
// The two todo payloads differ on purpose: byte-identical re-sends
// short-circuit (see TestTodo_DeclaresIdempotent) and never reach Run.
func TestAgent_TodoFunding_FundsCreationAndCompletion(t *testing.T) {
	client := &stubClient{responses: []llm.ChatResponse{
		todoCall("1", `[{"id":1,"state":"pending","text":"a"},{"id":2,"state":"pending","text":"b"}]`),
		todoCall("2", `[{"id":1,"state":"done","text":"a"},{"id":2,"state":"pending","text":"b"}]`),
		say("half"),
		say("still"),
		say("almost"),
		say("done"),
	}}
	a := New(Config{
		Client: client, Tools: NewRegistry(&scriptTodo{states: []todoPhase{
			{0, nil}, {0, []string{"a", "b"}}, {1, []string{"b"}},
		}}),
		System: "sys", SkipVerify: true, TodoFunding: true,
	})

	out, err := a.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "done" {
		t.Fatalf("out = %q", out)
	}
	if got := a.Report().MaxSteps; got != 25+8+4 {
		t.Fatalf("funded budget = %d, want 37", got)
	}
	if n := countInjected(client.lastHistory, "unchecked todo"); n != maxTodoBounces {
		t.Fatalf("bounced %d times, want %d", n, maxTodoBounces)
	}
}

// Ritual re-sends (byte-identical lists, the observed heartbeat pattern
// from the audit run) short-circuit instead of burning steps: one
// execution, then refusal. TodoFunding stays off here to isolate the
// short-circuit from funding.
func TestAgent_TodoIdenticalResend_ShortCircuits(t *testing.T) {
	fake := &scriptTodo{states: []todoPhase{{0, []string{"a"}}}}
	same := `[{"id":1,"state":"pending","text":"a"}]`
	client := &stubClient{responses: []llm.ChatResponse{
		todoCall("1", same),
		todoCall("2", same),
		say("half"),
		say("still"),
		say("almost"),
		say("done"),
	}}
	a := New(Config{
		Client: client, Tools: NewRegistry(fake),
		System: "sys", SkipVerify: true,
	})

	out, err := a.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "done" {
		t.Fatalf("out = %q", out)
	}
	if fake.calls != 1 {
		t.Fatalf("todo executed %d times, want 1", fake.calls)
	}
	// The refusal is a tool result, not a harness nudge: scan all roles.
	refused := 0
	for _, m := range client.lastHistory {
		if strings.Contains(m.Content, "already ran in step") {
			refused++
		}
	}
	if refused != 1 {
		t.Fatalf("short-circuit refusal %d times, want 1", refused)
	}
}

// All-done transition grants the delivery handshake (+2, cap-exempt)
// plus the report-now nudge. A read between the two closes proves the
// latch holds; reopening resets it for a second close.
func TestAgent_TodoClosing_GrantsHandshakePerEpisode(t *testing.T) {
	var runs int32
	todo := &scriptTodo{states: []todoPhase{
		{0, nil},
		{0, []string{"a", "b"}},
		{1, nil},
		{1, []string{"c"}},
		{2, nil},
	}}
	client := &stubClient{responses: []llm.ChatResponse{
		todoCall("1", `[{"id":1,"state":"pending","text":"a"}]`),
		todoCall("2", `[{"id":1,"state":"done","text":"a"}]`),
		readStep("r", "x.go"),
		todoCall("3", `[{"id":3,"state":"pending","text":"c"}]`),
		todoCall("4", `[{"id":3,"state":"done","text":"c"}]`),
		say("done"),
	}}
	a := New(Config{
		Client: client,
		Tools:  NewRegistry(todo, readStub{name: "read_file", runs: &runs}),
		System: "sys", SkipVerify: true, TodoFunding: true,
	})

	out, err := a.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "done" {
		t.Fatalf("out = %q", out)
	}
	// 8 (two opens) + 4 (one done) + 2 (close) + 4 (second done) + 2 (close).
	if got := a.Report().MaxSteps; got != 25+8+4+2+4+2 {
		t.Fatalf("funded budget = %d, want 45", got)
	}
	if n := countInjected(client.lastHistory, "steps left"); n != 2 {
		t.Fatalf("closing nudge %d times, want 2", n)
	}
}

// The handshake is exempt from the doubling cap: a run funded to 50
// that closes still gets its delivery round.
func TestAgent_TodoClosing_ExemptFromCap(t *testing.T) {
	big := make([]string, 50)
	for i := range big {
		big[i] = "item"
	}
	todo := &scriptTodo{states: []todoPhase{
		{0, nil},
		{0, big},
		{50, nil},
	}}
	client := &stubClient{responses: []llm.ChatResponse{
		todoCall("1", `[{"id":1,"state":"pending","text":"x"}]`),
		todoCall("2", `[{"id":1,"state":"done","text":"x"}]`),
		say("done"),
	}}
	a := New(Config{
		Client: client, Tools: NewRegistry(todo),
		System: "sys", SkipVerify: true, TodoFunding: true,
	})

	if _, err := a.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := a.Report().MaxSteps; got != 52 {
		t.Fatalf("funded budget = %d, want 52", got)
	}
	if n := countInjected(client.lastHistory, "steps left"); n != 1 {
		t.Fatalf("closing nudge %d times, want 1", n)
	}
}

// Fifty open items ask for 200 steps; the cap holds at one extra base.
func TestAgent_TodoFunding_CapsAtOneExtraBase(t *testing.T) {
	var items []string
	for i := 0; i < 50; i++ {
		items = append(items, "item")
	}
	var runs int32
	client := &stubClient{responses: []llm.ChatResponse{
		readStep("1", "a.go"),
		say("half"),
		say("still"),
		say("almost"),
		say("done"),
	}}
	a := New(Config{
		Client: client,
		Tools:  NewRegistry(readStub{name: "read_file", runs: &runs}, todoFake{open: items}),
		System: "sys", SkipVerify: true, TodoFunding: true,
	})

	if _, err := a.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := a.Report().MaxSteps; got != 50 {
		t.Fatalf("funded budget = %d, want 50", got)
	}
}

// Default off: same run without the flag keeps the base budget (the
// gate still bounces — funding and gating are independent).
func TestAgent_TodoFunding_DisabledByDefault(t *testing.T) {
	var runs int32
	client := &stubClient{responses: []llm.ChatResponse{
		readStep("1", "a.go"),
		say("half"),
		say("still"),
		say("almost"),
		say("done"),
	}}
	a := New(Config{
		Client: client,
		Tools:  NewRegistry(readStub{name: "read_file", runs: &runs}, todoFake{open: []string{"a", "b", "c"}}),
		System: "sys", SkipVerify: true,
	})

	if _, err := a.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := a.Report().MaxSteps; got != 25 {
		t.Fatalf("unfunded budget = %d, want 25", got)
	}
	if n := countInjected(client.lastHistory, "unchecked todo"); n != maxTodoBounces {
		t.Fatalf("bounced %d times, want %d", n, maxTodoBounces)
	}
}
