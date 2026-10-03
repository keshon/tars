package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/keshon/tars/internal/llm"
)

func say(text string) llm.ChatResponse {
	return llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: text}}
}

// writeStep is the tool call a worker is supposed to make instead of
// narrating that it is about to make one.
func writeStep(id, path string) llm.ChatResponse {
	args, _ := json.Marshal(map[string]string{"path": path, "content": "x"})
	return llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
		{ID: id, Name: "write_file", Arguments: args}}}}
}

func zeroWriteAgent(client llm.Client) *Agent {
	return New(Config{
		Client: client, Tools: NewRegistry(mutStub{"write_file"}), System: "sys",
		SkipVerify: true, VerifyOnZeroWrites: true,
	})
}

func countInjected(history []llm.Message, needle string) int {
	n := 0
	for _, m := range history {
		if m.Role == llm.RoleUser && strings.Contains(m.Content, needle) {
			n++
		}
	}
	return n
}

// The announce-without-write shape: a worker reads everything, says "let
// me write the file", and finishes with zero mutating calls. With
// VerifyOnZeroWrites the loop injects the verify round carrying the
// zero-writes hard fact, and the worker then does the write in context —
// one cheap round trip instead of a fresh fix worker.
func TestAgent_VerifyOnZeroWrites_FiresOnlyWhenNothingWritten(t *testing.T) {
	client := &stubClient{responses: []llm.ChatResponse{
		say("I am done (wrote nothing)"),
		writeStep("1", "src/main.ts"),
		say("wrote src/main.ts"),
	}}
	a := zeroWriteAgent(client)

	out, err := a.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "wrote src/main.ts" {
		t.Fatalf("out = %q", out)
	}
	if countInjected(client.lastHistory, "0 file-writing") != 1 {
		t.Fatal("zero-writes hard fact missing from the verify message")
	}
}

// The failure this exists to prevent (live, 2026-07-14, parseurl mission):
// the worker announced, took the verify nudge, announced again, and the
// SECOND announcement was accepted as the run's answer — three subtask
// attempts died in that shape. A finish with nothing written is refused
// for as long as writes are still expected.
func TestAgent_ZeroWriteFinish_RefusedUntilSomethingIsWritten(t *testing.T) {
	client := &stubClient{responses: []llm.ChatResponse{
		say("Now I have a thorough understanding. Let me write the plan.md file."),
		say("Let me create plan.md with the analysis documented."),
		say("I will now write plan.md."),
		writeStep("1", "plan.md"),
		say("wrote plan.md"),
	}}
	a := zeroWriteAgent(client)

	out, err := a.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "wrote plan.md" {
		t.Fatalf("out = %q — an announcement was accepted as the finish", out)
	}
	// Second and third announcements are refused; the first got the
	// verify round instead.
	if got := countInjected(client.lastHistory, "Nothing was written"); got != 2 {
		t.Fatalf("refusals = %d, want 2", got)
	}
	// The refusal quotes the model's own closing line back at it — a
	// generic nudge reads as boilerplate and gets answered with more
	// narration.
	if countInjected(client.lastHistory, "Let me create plan.md with the analysis documented.") == 0 {
		t.Fatal("refusal did not quote the model's own claim")
	}
}

// Refusing forever would just burn MaxSteps. After MaxZeroWriteRefusals
// the run ends and the mission layer takes over with the failing check
// output, which is better evidence than another nudge.
func TestAgent_ZeroWriteFinish_GivesUpAsError(t *testing.T) {
	client := &stubClient{responses: []llm.ChatResponse{
		say("let me write it"), say("let me write it"), say("let me write it"),
		say("let me write it"), say("let me write it"),
	}}
	a := zeroWriteAgent(client)

	_, err := a.Run(context.Background(), "task")
	if err == nil || !errors.Is(err, ErrMaxSteps) {
		t.Fatalf("Run err = %v, want wrapped max-steps", err)
	}
	// 1 verify round + MaxZeroWriteRefusals refusals, then the error —
	// bounded like before, but never accepted as success.
	if want := 2 + MaxZeroWriteRefusals; client.calls != want {
		t.Fatalf("calls = %d, want %d", client.calls, want)
	}
}

func TestAgent_OnNudge_KindsAndPrefix(t *testing.T) {
	client := &stubClient{responses: []llm.ChatResponse{
		say("x"), say("x"), say("x"), say("x"), say("x"),
	}}
	var kinds []string
	a := New(Config{
		Client: client, Tools: NewRegistry(mutStub{"write_file"}), System: "sys",
		SkipVerify: true, VerifyOnZeroWrites: true,
		OnNudge: func(kind, text string) {
			kinds = append(kinds, kind)
			if !strings.HasPrefix(text, "[harness] ") {
				t.Errorf("nudge without provenance: %q", text)
			}
		},
	})
	// The give-up error is expected; kinds are the assertion.
	_, _ = a.Run(context.Background(), "task")
	want := []string{"verify", "refusal", "refusal", "refusal"}
	if len(kinds) != len(want) {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("kinds = %v, want %v", kinds, want)
		}
	}
	// Every non-task user message in history carries the mark: the
	// model can no longer mistake harness text for the operator.
	for _, m := range client.lastHistory {
		if m.Role == llm.RoleUser && m.Content != "task" && !strings.HasPrefix(m.Content, "[harness] ") {
			t.Errorf("unmarked harness text: %q", m.Content)
		}
	}
}

func TestAgent_VerifyOnZeroWrites_SkippedAfterRealWrites(t *testing.T) {
	client := &stubClient{responses: []llm.ChatResponse{
		writeStep("1", "a.txt"),
		say("done"),
	}}
	a := zeroWriteAgent(client)

	if _, err := a.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if client.calls != 2 {
		t.Fatalf("calls = %d, want 2 — a worker that wrote files finishes without the extra round", client.calls)
	}
}
