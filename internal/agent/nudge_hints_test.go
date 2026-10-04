package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/keshon/tars/internal/llm"
)

// askFake offers ask_user without ever being called: the loop only needs
// the name in the registry to phrase the fatigue escalation concretely.
type askFake struct{}

func (askFake) Name() string            { return "ask_user" }
func (askFake) Description() string     { return "stub" }
func (askFake) Mode() ToolMode          { return Concurrent }
func (askFake) Schema() json.RawMessage { return json.RawMessage(`{}`) }
func (askFake) Run(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}

func budgetAgent(tools ...Tool) (*Agent, *int) {
	a := New(Config{
		Client: &stubClient{}, Tools: NewRegistry(tools...), System: "sys",
		ContextLimit: 1000,
	})
	w := 0
	return a, &w
}

// The delegate handoff is appended only for registries that offer it: a
// worker told to delegate burns steps on "unknown tool".
func TestBudgetHint_AppendedOnlyWithDelegate(t *testing.T) {
	with, w := budgetAgent(delegateStub{})
	got := with.budgetWarning(llm.Usage{PromptTokens: 950}, w)
	if !strings.Contains(got, "delegate_task") {
		t.Fatalf("delegate registry must name the handoff: %q", got)
	}
	without, w2 := budgetAgent()
	got2 := without.budgetWarning(llm.Usage{PromptTokens: 950}, w2)
	if strings.Contains(got2, "delegate_task") {
		t.Fatalf("worker registry must not name a tool it lacks: %q", got2)
	}
	if !strings.Contains(got2, "Wrap up now") {
		t.Fatalf("generic warning lost its imperative: %q", got2)
	}
}

// Same contract for the search-fatigue escalation and ask_user: eight
// novel reads (no tool-loop trip) earn the fatigue nudge, phrased for
// the registry at hand.
func TestFatigueHint_AppendedOnlyWithAskUser(t *testing.T) {
	reads := func() []llm.ChatResponse {
		out := []llm.ChatResponse{}
		for i := 0; i < 8; i++ {
			// Distinct paths with fresh bytes each: genuine exploration
			// (no tool-loop trip) that still never mutates.
			out = append(out, readStep(string(rune('a'+i)), "f"+"01234567"[i:i+1]+".go"))
		}
		return append(out, say("done"))
	}
	run := func(reg *Registry) []llm.Message {
		client := &stubClient{responses: reads()}
		a := New(Config{
			Client: client, Tools: reg, System: "sys", SkipVerify: true,
		})
		if _, err := a.Run(context.Background(), "task"); err != nil {
			t.Fatalf("Run: %v", err)
		}
		return client.lastHistory
	}

	var runs int32
	withAsk := run(NewRegistry(freshReadStub{runs: &runs}, askFake{}))
	if n := countInjected(withAsk, "ask the user directly"); n != 1 {
		t.Fatalf("ask registry: escalation %d times, want 1", n)
	}
	var runs2 int32
	withoutAsk := run(NewRegistry(freshReadStub{runs: &runs2}))
	if n := countInjected(withoutAsk, "ask the user directly"); n != 0 {
		t.Fatalf("worker registry: escalation %d times, want 0", n)
	}
	if n := countInjected(withoutAsk, "genuinely different approach"); n != 1 {
		t.Fatalf("generic fatigue missing: %d", n)
	}
}

// Truncation recovery names write_file only for writers: a read-only
// agent told to "write it in smaller pieces" can only obey by breaking
// its own registry.
func TestTruncatedHint_AppendedOnlyForWriters(t *testing.T) {
	run := func(reg *Registry) []llm.Message {
		client := &stubClient{responses: []llm.ChatResponse{
			{Message: llm.Message{Role: llm.RoleAssistant, Content: "partial…"}, FinishReason: "length"},
			say("done"),
		}}
		a := New(Config{
			Client: client, Tools: reg, System: "sys", SkipVerify: true,
		})
		if _, err := a.Run(context.Background(), "task"); err != nil {
			t.Fatalf("Run: %v", err)
		}
		return client.lastHistory
	}
	if h := run(NewRegistry(echoToolStub{name: "write_file"})); countInjected(h, "smaller pieces") != 1 {
		t.Fatal("writer must get the write-in-pieces advice")
	}
	if h := run(NewRegistry()); countInjected(h, "smaller pieces") != 0 {
		t.Fatal("read-only agent must not get write advice")
	}
}
