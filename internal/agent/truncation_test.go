package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/keshon/tars/internal/llm"
	"github.com/keshon/tars/internal/prompts"
)

// Live failure shape (Qwen3.6 run, 2026-07-11): the backend cut generation
// off after 38 tokens (finish_reason "length"), discarding the tool call
// the model had announced, and the loop accepted "Let me write the
// plan.md file…" as a final answer. A truncated response is never a finish.
func TestAgent_TruncatedFinish_NudgesAndContinues(t *testing.T) {
	client := &stubClient{responses: []llm.ChatResponse{
		{
			Message:      llm.Message{Role: llm.RoleAssistant, Content: "Let me write the file now."},
			FinishReason: "length",
		},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "actual answer"}, FinishReason: "stop"},
	}}
	a := New(Config{Client: client, Tools: NewRegistry(), System: "sys", SkipVerify: true})

	out, err := a.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "actual answer" {
		t.Fatalf("result = %q — the truncated stump must not be accepted as the answer", out)
	}
	var nudged bool
	for _, m := range client.lastHistory {
		if m.Role == llm.RoleUser && m.Content == harnessText(prompts.Truncated) {
			nudged = true
		}
	}
	if !nudged {
		t.Fatal("truncation nudge missing from history")
	}
}

func TestAgent_PersistentTruncation_BoundedByMaxSteps(t *testing.T) {
	client := &alwaysTruncatedClient{}
	a := New(Config{Client: client, Tools: NewRegistry(), System: "sys", SkipVerify: true, MaxSteps: 4})

	_, err := a.Run(context.Background(), "task")
	if err == nil || !errors.Is(err, ErrMaxSteps) {
		t.Fatalf("err = %v, want bounded max-steps failure, not an infinite nudge loop", err)
	}
	if client.calls != 4 {
		t.Fatalf("calls = %d, want exactly MaxSteps", client.calls)
	}
}

func TestAgent_ToolCallsWithLengthFinish_ExecuteNormally(t *testing.T) {
	// koboldcpp only returns fully parsed tool calls; length alongside
	// tool_calls means the calls that made it through are complete.
	args := json.RawMessage(`{"path":"a.txt","content":"x"}`)
	client := &stubClient{responses: []llm.ChatResponse{
		{
			Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
				{ID: "1", Name: "write_file", Arguments: args}}},
			FinishReason: "length",
		},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}, FinishReason: "stop"},
	}}
	a := New(Config{
		Client: client, Tools: NewRegistry(mutStub{"write_file"}),
		System: "sys", SkipVerify: true,
	})

	if _, err := a.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if a.LastRunMutations != 1 {
		t.Fatalf("mutations = %d, want 1 — the delivered tool call must execute", a.LastRunMutations)
	}
}

type alwaysTruncatedClient struct{ calls int }

func (c *alwaysTruncatedClient) Chat(context.Context, llm.ChatRequest) (llm.ChatResponse, error) {
	c.calls++
	return llm.ChatResponse{
		Message:      llm.Message{Role: llm.RoleAssistant, Content: "Let me just"},
		FinishReason: "length",
	}, nil
}
