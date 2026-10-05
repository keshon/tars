package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/keshon/tars/internal/llm"
)

func warningAgent(maxSteps int) *Agent {
	return New(Config{
		Client: &stubClient{}, Tools: NewRegistry(), System: "sys",
		MaxSteps: maxSteps, SkipVerify: true,
	})
}

func TestStepWarning_FiresOncePerThreshold(t *testing.T) {
	a := warningAgent(10)
	var st runState
	if got := a.stepWarning(&st, 6); got != "" {
		t.Fatalf("70%% must stay silent: %q", got)
	}
	n := a.stepWarning(&st, 7)
	if !strings.Contains(n, "8/10") || !strings.Contains(n, "2 steps left") {
		t.Fatalf("notice must name used/limit/left: %q", n)
	}
	if again := a.stepWarning(&st, 7); again != "" {
		t.Fatalf("notice must latch: %q", again)
	}
	w := a.stepWarning(&st, 8)
	if !strings.Contains(w, "9/10") || !strings.Contains(w, "1 steps left") {
		t.Fatalf("warning must name used/limit/left: %q", w)
	}
	if again := a.stepWarning(&st, 9); again != "" {
		t.Fatalf("warning must latch: %q", again)
	}
}

func TestStepWarning_FollowsFundedLimit(t *testing.T) {
	a := warningAgent(10)
	st := &runState{todoFunded: 10, warnedSteps: 75}
	// 15/20 = 75% already warned: silent here, fires at 18/20.
	if got := a.stepWarning(st, 14); got != "" {
		t.Fatalf("must respect funded limit and latch: %q", got)
	}
	if got := a.stepWarning(st, 17); !strings.Contains(got, "18/20") {
		t.Fatalf("must fire at 90%% of funded limit: %q", got)
	}
}

func TestStepWarning_ZeroLimitSilent(t *testing.T) {
	a := &Agent{}
	if got := a.stepWarning(&runState{}, 5); got != "" {
		t.Fatalf("zero limit must stay silent: %q", got)
	}
}

// An 11-step run reads its way down with fresh results each time (no
// repeat/stuck/tool-loop arms to steal the interject slot): notice once
// at 9/11, warning once at 10/11, then finishes on the last step.
func TestStepWarning_FiresInRun(t *testing.T) {
	var runs int32
	resps := make([]llm.ChatResponse, 0, 11)
	for i := 0; i < 10; i++ {
		resps = append(resps, readStep(string(rune('a'+i)), "f"+"0123456789"[i:i+1]+".go"))
	}
	resps = append(resps, say("done"))
	client := &stubClient{responses: resps}
	a := New(Config{
		Client: client, Tools: NewRegistry(freshReadStub{runs: &runs}),
		System: "sys", MaxSteps: 11, SkipVerify: true,
	})

	out, err := a.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "done" {
		t.Fatalf("out = %q", out)
	}
	if n := countInjected(client.lastHistory, "Step budget"); n != 2 {
		t.Fatalf("step warnings fired %d times, want 2", n)
	}
	for _, want := range []string{"9/11", "10/11"} {
		found := false
		for _, m := range client.lastHistory {
			if strings.Contains(m.Content, want) {
				found = true
			}
		}
		if !found {
			t.Fatalf("warning %q missing from history", want)
		}
	}
}

// The death path snapshots open todos for the operator gate: a run
// that dies at MaxSteps with acknowledged work left must say so. Tool
// steps (not narrations, which would finish) burn the budget.
func TestReport_OpenTodosSnapshotOnMaxSteps(t *testing.T) {
	var runs int32
	client := &stubClient{responses: []llm.ChatResponse{
		readStep("1", "a.go"),
		readStep("2", "a.go"),
		readStep("3", "a.go"),
		readStep("4", "a.go"),
		readStep("5", "a.go"),
		say("unfinished: c.go pending"),
	}}
	a := New(Config{
		Client: client,
		Tools:  NewRegistry(readStub{name: "read_file", runs: &runs}, todoFake{open: []string{"a", "b"}}),
		System: "sys", MaxSteps: 5, SkipVerify: true,
	})

	_, err := a.Run(context.Background(), "task")
	if err == nil || !errors.Is(err, ErrMaxSteps) {
		t.Fatalf("expected max-steps death, got %v", err)
	}
	got := a.Report().OpenTodos
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("OpenTodos = %v, want [a b]", got)
	}
}

func TestReport_OpenTodosEmptyWithoutList(t *testing.T) {
	client := &stubClient{responses: []llm.ChatResponse{say("hi")}}
	a := New(Config{
		Client: client, Tools: NewRegistry(), System: "sys", SkipVerify: true,
	})

	if _, err := a.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := a.Report().OpenTodos; len(got) != 0 {
		t.Fatalf("OpenTodos = %v, want empty", got)
	}
}
