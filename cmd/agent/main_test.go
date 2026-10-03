package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keshon/tars/internal/agent"
	"github.com/keshon/tars/internal/events"
	"github.com/keshon/tars/internal/permission"
	"github.com/keshon/tars/internal/workspace"
)

func testWS(t *testing.T) *workspace.Workspace {
	t.Helper()
	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return ws
}

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
	ws := testWS(t)
	gate := permissionGate(false, channelSuspender("y"), ws)
	if eff, err := gate("run_shell", "go test ./...", nil); err != nil || eff != permission.Allow {
		t.Fatalf("allow: eff=%v err=%v", eff, err)
	}

	gate = permissionGate(false, channelSuspender("n"), ws)
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
	gate := permissionGate(true, suspend, testWS(t))
	if eff, err := gate("run_shell", "anything", nil); err != nil || eff != permission.Deny {
		t.Fatalf("auto-deny: eff=%v err=%v", eff, err)
	}
	if calls != 0 {
		t.Fatal("auto-deny must not consult the transport")
	}
}

func TestPermissionGate_TransportErrorDenies(t *testing.T) {
	gate := permissionGate(false, agent.ClosedSuspender("headless test"), testWS(t))
	if eff, err := gate("run_shell", "x", nil); err == nil || eff != permission.Deny {
		t.Fatalf("transport error must fail closed: eff=%v err=%v", eff, err)
	}
}

func TestPermissionGate_PromptCarriesPreview(t *testing.T) {
	// The approver sees evidence, not just a tool name: a patch gate
	// prompt must contain the diff the call would produce.
	dir := t.TempDir()
	ws, err := workspace.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var gotPrompt string
	suspend := func(_ context.Context, req agent.SuspendRequest) (agent.SuspendReply, error) {
		gotPrompt = req.Prompt
		return agent.SuspendReply{Answer: "n"}, nil
	}
	gate := permissionGate(false, suspend, ws)
	_, _ = gate("patch_file", "a.txt", json.RawMessage(`{"path":"a.txt","old_content":"two","new_content":"TWO"}`))
	if !strings.Contains(gotPrompt, "-two") || !strings.Contains(gotPrompt, "+TWO") {
		t.Fatalf("prompt lacks diff: %q", gotPrompt)
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

func TestStreamEnabled(t *testing.T) {
	for _, tc := range []struct {
		stream, tui bool
		mode        string
		want        bool
	}{
		{false, false, "print", false},
		{true, false, "print", true},
		{false, true, "print", true},
		{true, true, "print", true},
		{true, false, "json", false},
		{false, true, "json", false},
	} {
		if got := streamEnabled(tc.stream, tc.mode, tc.tui); got != tc.want {
			t.Errorf("streamEnabled(%v,%q,%v) = %v, want %v",
				tc.stream, tc.mode, tc.tui, got, tc.want)
		}
	}
}
