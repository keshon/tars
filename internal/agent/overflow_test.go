package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/keshon/tars/internal/llm"
)

// overflowOnce returns an overflow error on the first call, then answers.
type overflowOnce struct {
	calls int
}

func (o *overflowOnce) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	o.calls++
	if o.calls == 1 {
		return llm.ChatResponse{}, &llm.APIError{Status: 400, StatusText: "bad", Body: "exceeds the context window", Overflow: true}
	}
	return llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: "recovered"}}, nil
}

func TestRun_RecoversFromOverflowByCompacting(t *testing.T) {
	c := &overflowOnce{}
	a := New(Config{
		Client:           c,
		Tools:            NewRegistry(echoToolStub{}),
		System:           "sys",
		ContextLimit:     1000,
		CompactKeepSteps: 2,
		SkipVerify:       true,
	})
	history := []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "task"},
	}
	for i := 0; i < 6; i++ {
		id := "c"
		history = append(history,
			llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Name: "echo", Arguments: json.RawMessage(`{}`)}}},
			llm.Message{Role: llm.RoleTool, ToolCallID: id, Content: "ok"},
		)
	}
	got, err := a.Resume(context.Background(), history, "continue")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if got != "recovered" {
		t.Fatalf("got %q, want recovered", got)
	}
	if c.calls != 2 {
		t.Fatalf("calls=%d, want 2 (overflow must not retry)", c.calls)
	}
}

// Probe 18 failed live on exactly this shape: the first model call of a
// fresh run overflowed, history held only system+task (nothing to
// compact), and the run died instead of continuing with the nudge.
func TestRun_OverflowOnFirstCallContinues(t *testing.T) {
	c := &overflowOnce{}
	a := New(Config{
		Client:           c,
		Tools:            NewRegistry(echoToolStub{}),
		System:           "sys",
		ContextLimit:     1000,
		CompactKeepSteps: 2,
		SkipVerify:       true,
	})
	got, err := a.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != "recovered" {
		t.Fatalf("got %q, want recovered", got)
	}
}

// scriptedClient replays responses and errors in order.
type scriptedClient struct {
	calls int
	steps []any // llm.ChatResponse or error
}

func (s *scriptedClient) Chat(_ context.Context, _ llm.ChatRequest) (llm.ChatResponse, error) {
	s.calls++
	switch st := s.steps[s.calls-1].(type) {
	case error:
		return llm.ChatResponse{}, st
	default:
		return st.(llm.ChatResponse), nil
	}
}

// Overflow recovery rewrites history, so the repeat caches must not
// survive it: a read refused as "already ran" before the overflow has
// no citable result afterwards and must re-execute.
func TestRun_OverflowRecoveryClearsRepeatCaches(t *testing.T) {
	var runs int32
	c := &scriptedClient{steps: []any{
		readStep("1", "a.go"),
		readStep("2", "a.go"),
		&llm.APIError{Status: 400, StatusText: "bad", Body: "exceeds the context window", Overflow: true},
		readStep("3", "a.go"),
		say("done"),
	}}
	a := New(Config{
		Client:           c,
		Tools:            NewRegistry(readStub{name: "read_file", runs: &runs}),
		System:           "sys",
		ContextLimit:     1000,
		CompactKeepSteps: 2,
		SkipVerify:       true,
	})
	out, err := a.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "done" {
		t.Fatalf("out = %q", out)
	}
	// Three identical reads, two executions: the middle one was refused
	// from cache (proving it was populated), the last one re-ran because
	// overflow recovery cleared it.
	if runs != 2 {
		t.Fatalf("runs = %d, want 2", runs)
	}
	if c.calls != 5 {
		t.Fatalf("calls = %d, want 5", c.calls)
	}
}
