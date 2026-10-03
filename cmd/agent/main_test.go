package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/keshon/tars/internal/agent"
	"github.com/keshon/tars/internal/events"
	"github.com/keshon/tars/internal/permission"
)

// channelSuspender answers from a script, not stdin: the proof that gates
// no longer need a terminal. Zero stdin reads anywhere in this file.
func channelSuspender(answers ...string) agent.Suspender {
	i := 0
	return func(_ context.Context, req agent.SuspendRequest) (agent.SuspendReply, error) {
		if i >= len(answers) {
			return agent.SuspendReply{}, context.DeadlineExceeded
		}
		ans := answers[i]
		i++
		return agent.SuspendReply{Answer: ans}, nil
	}
}

func TestPermissionGate_ChannelAnswers(t *testing.T) {
	gate := permissionGate(false, channelSuspender("y"))
	if eff, err := gate("run_shell", "go test ./...", nil); err != nil || eff != permission.Allow {
		t.Fatalf("allow: eff=%v err=%v", eff, err)
	}

	gate = permissionGate(false, channelSuspender("n"))
	if eff, err := gate("run_shell", "rm -rf /", nil); err == nil || eff != permission.Deny {
		t.Fatalf("deny: eff=%v err=%v", eff, err)
	}
}

func TestPermissionGate_AutoDenyNeverSuspends(t *testing.T) {
	calls := 0
	suspend := func(context.Context, agent.SuspendRequest) (agent.SuspendReply, error) {
		calls++
		return agent.SuspendReply{Answer: "y"}, nil
	}
	gate := permissionGate(true, suspend)
	if eff, err := gate("run_shell", "anything", nil); err != nil || eff != permission.Deny {
		t.Fatalf("auto-deny: eff=%v err=%v", eff, err)
	}
	if calls != 0 {
		t.Fatal("auto-deny must not consult the transport")
	}
}

func TestPermissionGate_TransportErrorDenies(t *testing.T) {
	gate := permissionGate(false, agent.ClosedSuspender("headless test"))
	if eff, err := gate("run_shell", "x", nil); err == nil || eff != permission.Deny {
		t.Fatalf("transport error must fail closed: eff=%v err=%v", eff, err)
	}
}

func TestGateEvents_Vocabulary(t *testing.T) {
	// Pins the event contract P9 and RPC clients consume: every gate
	// announces kind+id on suspend and on answer. If these keys change,
	// every consumer breaks silently.
	var buf bytes.Buffer
	em := events.New(&buf)
	em.Emit("awaiting_input", map[string]any{"kind": string(agent.SuspendAsk), "id": "q1"})
	em.Emit("input_answered", map[string]any{"kind": string(agent.SuspendAsk), "id": "q1"})
	out := buf.String()
	for _, want := range []string{`"event":"awaiting_input"`, `"event":"input_answered"`,
		`"kind":"ask_user"`, `"id":"q1"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s in %s", want, out)
		}
	}
}
