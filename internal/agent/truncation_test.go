package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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
	if client.calls != 5 {
		t.Fatalf("calls = %d, want MaxSteps + 1 closing turn", client.calls)
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

// Truncated arguments (generation cut off mid-call) are stumps, not
// calls: executing them fails arg parsing here and 500s backends that
// parse server-side. Refuse with reissue-smaller guidance instead, and
// keep going on the next response.
func TestAgent_TruncatedArgs_RefusedWithGuidance(t *testing.T) {
	var runs int32
	client := &stubClient{responses: []llm.ChatResponse{
		{
			Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
				{ID: "1", Name: "write_file", Arguments: json.RawMessage(`{"path":"a.txt","content":"xxx`)}},
			},
			FinishReason: "length",
		},
		say("done"),
	}}
	a := New(Config{
		Client: client, Tools: NewRegistry(countingWriteStub{runs: &runs}),
		System: "sys", SkipVerify: true,
	})

	out, err := a.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "done" {
		t.Fatalf("result = %q, want the follow-up answer", out)
	}
	if runs != 0 {
		t.Fatalf("truncated call executed %d times, want 0", runs)
	}
	found := false
	for _, m := range client.lastHistory {
		if m.Role == llm.RoleTool && strings.Contains(m.Content, "truncated") {
			found = true
		}
	}
	if !found {
		t.Fatal("no truncated-args guidance reached the model")
	}
}

// A mixed batch executes the valid calls and refuses only the stumps.
func TestAgent_TruncatedArgs_MixedBatchExecutesValid(t *testing.T) {
	var runs int32
	writeArgs, _ := json.Marshal(map[string]string{"path": "b.txt", "content": "ok"})
	client := &stubClient{responses: []llm.ChatResponse{
		{
			Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
				{ID: "1", Name: "write_file", Arguments: writeArgs},
				{ID: "2", Name: "write_file", Arguments: json.RawMessage(`{"path":"c.txt","content":"xxx`)}},
			},
			FinishReason: "length",
		},
		say("done"),
	}}
	a := New(Config{
		Client: client, Tools: NewRegistry(countingWriteStub{runs: &runs}),
		System: "sys", SkipVerify: true,
	})

	if _, err := a.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if runs != 1 {
		t.Fatalf("valid call executed %d times, want 1", runs)
	}
	var ok, refused bool
	for _, m := range client.lastHistory {
		if m.Role != llm.RoleTool {
			continue
		}
		if strings.Contains(m.Content, "wrote ok") {
			ok = true
		}
		if strings.Contains(m.Content, "truncated") {
			refused = true
		}
	}
	if !ok || !refused {
		t.Fatalf("want one executed result and one truncation refusal (ok=%v refused=%v)", ok, refused)
	}
}
