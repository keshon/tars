package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/keshon/tars/internal/llm"
)

func TestStepTag_RendersLiveBudget(t *testing.T) {
	a := &Agent{cfg: Config{MaxSteps: 25}}
	st := &runState{}
	if got := a.stepTag(st, 0); got != " (step 1/25, 24 left)" {
		t.Fatalf("stepTag(0) = %q", got)
	}
	st.todoFunded = 25
	if got := a.stepTag(st, 30); got != " (step 31/50, 19 left)" {
		t.Fatalf("funded stepTag(30) = %q", got)
	}
}

func TestStepTag_ZeroLimitSilent(t *testing.T) {
	a := &Agent{cfg: Config{}}
	if got := a.stepTag(&runState{}, 0); got != "" {
		t.Fatalf("zero-limit stepTag = %q, want empty", got)
	}
}

// Decision-point messages must carry the live counter: a stuck
// escalation fired at step index 1 of a 25-budget run reads 2/25.
func TestStepTag_StuckEscalationCarriesCounter(t *testing.T) {
	client := &stubClient{responses: []llm.ChatResponse{
		failStep("1", "a.go"),
		failStep("2", "b.go"),
		say("giving up"),
	}}
	a := New(Config{
		Client: client, Tools: NewRegistry(errStub{"flaky"}), System: "sys",
		SkipVerify: true, MaxStuckSteps: 2, MaxExploratorySteps: 2,
	})

	if _, err := a.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	found := false
	for _, m := range client.lastHistory {
		if m.Role == llm.RoleUser && strings.Contains(m.Content, "(step 2/25") {
			found = true
		}
	}
	if !found {
		t.Fatal("stuck escalation missing live budget counter")
	}
}

// Same contract on the verify round: one successful read, then a finish
// attempt trips verify at step index 1.
func TestStepTag_VerifyRoundCarriesCounter(t *testing.T) {
	var runs int32
	client := &stubClient{responses: []llm.ChatResponse{
		readStep("1", "a.go"),
		say("done"),
		say("confirmed"),
	}}
	a := New(Config{
		Client: client, Tools: NewRegistry(readStub{name: "read_file", runs: &runs}), System: "sys",
	})

	if _, err := a.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := countInjected(client.lastHistory, "(step 2/25"); n != 1 {
		t.Fatalf("verify round carried counter %d times, want 1", n)
	}
}
