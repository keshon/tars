package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/keshon/tars/internal/llm"
)

// wrapClient serves canned responses, records per-call tool counts, and
// optionally fails every call after failAfter successes.
type wrapClient struct {
	responses  []llm.ChatResponse
	calls      int
	toolCounts []int
	failAfter  int
}

func (c *wrapClient) Chat(_ context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	c.calls++
	if c.failAfter > 0 && c.calls > c.failAfter {
		return llm.ChatResponse{}, errors.New("backend down")
	}
	c.toolCounts = append(c.toolCounts, len(req.Tools))
	return c.responses[c.calls-1], nil
}

// Budget death captures a closing summary without changing the error:
// two reads burn the budget, the third call is the wrap-up turn.
func TestAgent_WrapUp_FiresOnceOnBudgetDeath(t *testing.T) {
	var runs int32
	client := &wrapClient{responses: []llm.ChatResponse{
		readStep("1", "a.go"),
		readStep("2", "b.go"),
		say("wrap answer"),
	}}
	a := New(Config{
		Client: client, Tools: NewRegistry(readStub{name: "read_file", runs: &runs}),
		System: "sys", MaxSteps: 2, SkipVerify: true,
	})

	_, err := a.Run(context.Background(), "task")
	if err == nil || !errors.Is(err, ErrMaxSteps) {
		t.Fatalf("Run err = %v, want wrapped max-steps", err)
	}
	if got := a.Report().WrapUp; got != "wrap answer" {
		t.Fatalf("WrapUp = %q, want the closing summary", got)
	}
	if client.calls != 3 {
		t.Fatalf("calls = %d, want 3 (2 steps + 1 wrap-up turn)", client.calls)
	}
	if len(client.toolCounts) != 3 || client.toolCounts[2] == 0 {
		t.Fatalf("wrap-up turn stripped tools: %v", client.toolCounts)
	}
}

// A failed closing call must not mask the budget error or invent text.
func TestAgent_WrapUp_SkippedOnChatError(t *testing.T) {
	var runs int32
	client := &wrapClient{
		responses: []llm.ChatResponse{
			readStep("1", "a.go"),
		},
		failAfter: 1,
	}
	a := New(Config{
		Client: client, Tools: NewRegistry(readStub{name: "read_file", runs: &runs}),
		System: "sys", MaxSteps: 1, SkipVerify: true,
	})

	_, err := a.Run(context.Background(), "task")
	if err == nil || !errors.Is(err, ErrMaxSteps) {
		t.Fatalf("Run err = %v, want wrapped max-steps", err)
	}
	if got := a.Report().WrapUp; got != "" {
		t.Fatalf("WrapUp = %q, want empty on closing-call failure", got)
	}
	if !strings.Contains(err.Error(), "without finishing") {
		t.Fatalf("original error lost: %v", err)
	}
}
