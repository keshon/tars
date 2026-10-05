package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/keshon/tars/internal/llm"
)

// alwaysFails stands in for run_shell running a command that cannot
// succeed — a check whose form is wrong, a test file that will not
// compile. It counts executions so the test can prove the loop stopped
// running it, not merely stopped reporting it.
type alwaysFails struct {
	name string
	runs *int32
}

func (a alwaysFails) Name() string          { return a.name }
func (alwaysFails) Description() string     { return "stub" }
func (alwaysFails) Mode() ToolMode          { return Exclusive }
func (alwaysFails) Schema() json.RawMessage { return json.RawMessage(`{}`) }
func (a alwaysFails) Run(context.Context, json.RawMessage) (string, error) {
	atomic.AddInt32(a.runs, 1)
	return "", fmt.Errorf("command failed: exit status 1")
}

func shellStep(id, command string) llm.ChatResponse {
	args, _ := json.Marshal(map[string]string{"command": command})
	return llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
		{ID: id, Name: "run_shell", Arguments: args}}}}
}

// The parseurl shape (live): a worker ran the same broken `go test`
// command seven times. run_shell already returns an error on a non-zero
// exit, so the stuck counter was climbing and nudges were firing the
// whole time — the model ignored every one of them. A refusal cannot be
// ignored.
func TestAgent_IdenticalFailingCall_RefusedAfterTolerance(t *testing.T) {
	var runs int32
	const cmd = "go test ./internal/tools/parseurl_test.go"
	client := &stubClient{responses: []llm.ChatResponse{
		shellStep("1", cmd),
		shellStep("2", cmd),
		shellStep("3", cmd),
		shellStep("4", cmd),
		shellStep("5", cmd),
		say("giving up"),
	}}
	a := New(Config{
		Client: client, Tools: NewRegistry(alwaysFails{"run_shell", &runs}),
		System: "sys", SkipVerify: true,
	})

	if _, err := a.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := maxIdenticalAttempts - 1; int(runs) != want {
		t.Errorf("command executed %d times, want %d — the loop kept running a call "+
			"that could not succeed", runs, want)
	}
	if countInjected(client.lastHistory, "has already failed") != 0 {
		// The refusal is a tool result, not an injected user message.
		t.Error("refusal was delivered as a nudge instead of a tool error")
	}
	var refused bool
	for _, m := range client.lastHistory {
		if m.Role == llm.RoleTool && strings.Contains(m.Content, "has already failed") {
			refused = true
		}
	}
	if !refused {
		t.Error("no refusal reached the model as a tool result")
	}
}

// outputFails stands in for run_shell running a command that fails WITH
// output — compiler errors, test failures. The loop must keep the text
// alongside the error: dropping it blinds the model to what actually
// went wrong (live: a failed go build surfaced only as "exit status 1",
// forcing the model into echo-marker workarounds to see anything).
type outputFails struct{ name string }

func (o outputFails) Name() string          { return o.name }
func (outputFails) Description() string     { return "stub" }
func (outputFails) Mode() ToolMode          { return Exclusive }
func (outputFails) Schema() json.RawMessage { return json.RawMessage(`{}`) }
func (outputFails) Run(context.Context, json.RawMessage) (string, error) {
	return "main.go:10: undefined: foo", fmt.Errorf("command failed: exit status 1")
}

func TestAgent_FailingCall_KeepsOutput(t *testing.T) {
	client := &stubClient{responses: []llm.ChatResponse{
		shellStep("1", "go build ./..."),
		say("done"),
	}}
	a := New(Config{
		Client: client, Tools: NewRegistry(outputFails{"run_shell"}),
		System: "sys", SkipVerify: true,
	})

	if _, err := a.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	found := false
	for _, m := range client.lastHistory {
		if m.Role == llm.RoleTool &&
			strings.Contains(m.Content, "undefined: foo") &&
			strings.Contains(m.Content, "exit status 1") {
			found = true
		}
	}
	if !found {
		t.Fatal("tool result dropped failing-command output; history shows error only")
	}
}

// The tolerance exists for genuinely time-dependent retries: a check
// against a server that is still starting fails and then succeeds, and
// start_background is Concurrent so it does not clear the cache.
func TestAgent_FailingCall_ToleratesRetryBeforeRefusing(t *testing.T) {
	var runs int32
	client := &stubClient{responses: []llm.ChatResponse{
		shellStep("1", "curl localhost:9876"),
		shellStep("2", "curl localhost:9876"),
		say("done"),
	}}
	a := New(Config{
		Client: client, Tools: NewRegistry(alwaysFails{"run_shell", &runs}),
		System: "sys", SkipVerify: true,
	})

	if _, err := a.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if runs != 2 {
		t.Errorf("executed %d times, want 2 — a second attempt must still be allowed", runs)
	}
}

// A different command is a different call, however many times its
// neighbour failed.
func TestAgent_FailingCall_DoesNotBlockDifferentCommands(t *testing.T) {
	var runs int32
	client := &stubClient{responses: []llm.ChatResponse{
		shellStep("1", "go test ./a"),
		shellStep("2", "go test ./a"),
		shellStep("3", "go test ./a"),
		shellStep("4", "go test ./b"),
		say("done"),
	}}
	a := New(Config{
		Client: client, Tools: NewRegistry(alwaysFails{"run_shell", &runs}),
		System: "sys", SkipVerify: true,
	})

	if _, err := a.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// three attempts at ./a (the third refused before running, so two
	// executions) plus one at ./b
	if runs != 3 {
		t.Errorf("executed %d times, want 3 — a distinct command was blocked", runs)
	}
}
