package api

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keshon/tars/internal/agent"
	"github.com/keshon/tars/internal/events"
	"github.com/keshon/tars/internal/llm"
	"github.com/keshon/tars/internal/roles"
	"github.com/keshon/tars/internal/tools"
	"github.com/keshon/tars/internal/workspace"
)

func testEnv(t *testing.T, client llm.Client, record func(string, int, llm.Message)) roles.Env {
	t.Helper()
	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return roles.Env{
		Client: client, WS: ws, Procs: tools.NewBackgroundProcesses(),
		OnStep: record,
	}
}

func TestNew_Validates(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("empty task must fail")
	}
	ws, _ := workspace.New(t.TempDir())
	if _, err := New(Config{Task: "x", Env: roles.Env{WS: ws}}); err == nil {
		t.Fatal("nil client must fail")
	}
	if _, err := New(Config{Task: "x", Env: roles.Env{Client: stubOK{}, WS: ws}}); err != nil {
		t.Fatalf("valid config: %v", err)
	}
}

type stubOK struct{}

func (stubOK) Chat(_ context.Context, _ llm.ChatRequest) (llm.ChatResponse, error) {
	return llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}}, nil
}

func TestSession_RunEmitsSameSchemaAsEmitter(t *testing.T) {
	var events []Event
	s, err := New(Config{
		Task: "task",
		Env:  testEnv(t, stubOK{}, nil),
		OnEvent: func(ev Event) {
			events = append(events, ev)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "done" {
		t.Fatalf("result = %q", out)
	}
	// Framing is the transport's job: Session streams steps, results,
	// usage, and harness nudges — nothing else. (The stub answers "done"
	// to everything including the verify round, so more than one step
	// event is normal here. The stub reports zero usage, so usage events
	// carry zeros.)
	seenStep, seenUsage, seenNudge := false, false, false
	for _, ev := range events {
		switch ev.Name {
		case "step":
			seenStep = true
		case "usage":
			seenUsage = true
			if _, ok := ev.Fields["prompt"]; !ok {
				t.Fatalf("usage without prompt: %+v", ev)
			}
		case "nudge":
			seenNudge = true
			if _, ok := ev.Fields["kind"]; !ok {
				t.Fatalf("nudge without kind: %+v", ev)
			}
			if text, _ := ev.Fields["text"].(string); !strings.HasPrefix(text, "[harness] ") {
				t.Fatalf("nudge without provenance: %+v", ev)
			}
		default:
			t.Fatalf("events = %+v, want steps, usage and nudges only", events)
		}
	}
	if !seenStep || !seenUsage {
		t.Fatalf("steps and usage both expected: %+v", events)
	}
	if !seenNudge {
		t.Fatalf("verify nudge expected on the wire: %+v", events)
	}
}

func TestSession_AskGoesThroughSuspender(t *testing.T) {
	asked := 0
	client := &askThenDone{}
	s, err := New(Config{
		Task: "task",
		Env:  testEnv(t, client, nil),
		Answer: func(_ context.Context, req agent.SuspendRequest) (agent.SuspendReply, error) {
			if req.Kind != agent.SuspendAsk {
				t.Errorf("kind = %s", req.Kind)
			}
			asked++
			return agent.SuspendReply{Answer: "assumed"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if asked != 1 {
		t.Fatalf("ask suspender called %dx", asked)
	}
}

type askThenDone struct{ calls int }

func (s *askThenDone) Chat(_ context.Context, _ llm.ChatRequest) (llm.ChatResponse, error) {
	s.calls++
	if s.calls == 1 {
		return llm.ChatResponse{Message: llm.Message{
			Role: llm.RoleAssistant,
			ToolCalls: []llm.ToolCall{{
				ID: "q1", Name: "ask_user",
				Arguments: json.RawMessage(`{"question":"which?"}`),
			}},
		}}, nil
	}
	return llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}}, nil
}

func TestSession_HeadlessFailsClosed(t *testing.T) {
	s, err := New(Config{
		Task: "task",
		Env:  testEnv(t, &askThenDone{}, nil),
		// No Answer: ClosedSuspender must fail, never hang.
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = s.Run(context.Background())
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("headless run hung instead of failing closed")
	}
}

func TestEmitStep_MatchesEmitterEncoding(t *testing.T) {
	// The schema contract both directions: EmitStep output must parse as
	// the same JSON -mode json prints, and a Session callbackWriter must
	// decode it back field-identical.
	msg := llm.Message{
		Role:    llm.RoleAssistant,
		Content: "working",
		ToolCalls: []llm.ToolCall{
			{ID: "c1", Name: "read_file", Arguments: json.RawMessage(`{"path":"a.txt"}`)},
		},
	}
	var buf bytes.Buffer
	EmitStep(events.New(&buf), "w1", 3, msg, false)

	var got []Event
	w := &callbackWriter{onEvent: func(ev Event) { got = append(got, ev) }}
	if _, err := w.Write(buf.Bytes()); err != nil {
		t.Fatalf("write: %v", err)
	}
	if len(got) != 1 || got[0].Name != "step" {
		t.Fatalf("events = %+v", got)
	}
	if got[0].Seq != 1 {
		t.Fatalf("seq = %d", got[0].Seq)
	}
	fields := got[0].Fields
	if fields["label"] != "w1" {
		t.Fatalf("label = %v", fields["label"])
	}
	if fields["text"] != "working" {
		t.Fatalf("text = %v", fields["text"])
	}
	calls, ok := fields["tool_calls"].([]any)
	if !ok || len(calls) != 1 {
		t.Fatalf("tool_calls = %v", fields["tool_calls"])
	}
	call, ok := calls[0].(map[string]any)
	if !ok || call["name"] != "read_file" {
		t.Fatalf("call = %v", calls[0])
	}
}

func TestEmitStep_IncludesCallID(t *testing.T) {
	var buf bytes.Buffer
	EmitStep(events.New(&buf), "run", 1, llm.Message{
		ToolCalls: []llm.ToolCall{{ID: "c9", Name: "read_file", Arguments: json.RawMessage(`{}`)}},
	}, false)
	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec); err != nil {
		t.Fatalf("not json: %v", err)
	}
	calls, ok := rec["tool_calls"].([]any)
	if !ok || len(calls) != 1 {
		t.Fatalf("tool_calls = %v", rec["tool_calls"])
	}
	call, ok := calls[0].(map[string]any)
	if !ok || call["id"] != "c9" {
		t.Fatalf("call = %v", calls[0])
	}
}

func TestEmitStep_HarnessReplyFlag(t *testing.T) {
	var buf bytes.Buffer
	EmitStep(events.New(&buf), "run", 1, llm.Message{Content: "hi"}, true)
	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec); err != nil {
		t.Fatalf("not json: %v", err)
	}
	if rec["harness_reply"] != true {
		t.Fatalf("flag missing: %v", rec)
	}
	var buf2 bytes.Buffer
	EmitStep(events.New(&buf2), "run", 1, llm.Message{Content: "hi"}, false)
	var rec2 map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf2.Bytes()), &rec2); err != nil {
		t.Fatalf("not json: %v", err)
	}
	if _, ok := rec2["harness_reply"]; ok {
		t.Fatalf("flag must be omitted when false: %v", rec2)
	}
}

func TestSession_HarnessReplyFlaggedAfterNudge(t *testing.T) {
	var events []Event
	s, err := New(Config{
		Task: "task",
		Env:  testEnv(t, stubOK{}, nil),
		OnEvent: func(ev Event) {
			events = append(events, ev)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The step right after a nudge carries the flag when text-only.
	nudgeAt := -1
	for i, ev := range events {
		if ev.Name == "nudge" {
			nudgeAt = i
			break
		}
	}
	if nudgeAt < 0 {
		t.Fatalf("no nudge in %d events", len(events))
	}
	found := false
	for _, ev := range events[nudgeAt+1:] {
		if ev.Name != "step" {
			continue
		}
		if ev.Fields["harness_reply"] != true {
			t.Fatalf("post-nudge step missing flag: %+v", ev.Fields)
		}
		found = true
		break
	}
	if !found {
		t.Fatal("no step followed the nudge")
	}
}

func TestFindingDrain_DedupesAcrossTiers(t *testing.T) {
	var buf bytes.Buffer
	em := events.New(&buf)
	prev := 0
	drain := newFindingDrain(em, func(string, string, int, string) { prev++ })
	drain.reportPerEdit("gofmt", "a.go", 3, "not gofmt-clean")
	dir := t.TempDir()
	ws, err := workspace.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nfunc A()  {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The session-end sweep sees the same dirty file: reported once.
	drain.sweep(ws, []string{"a.go"})
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("not json: %v", err)
		}
		if rec["event"] == "finding" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d finding events, want exactly 1 (cross-tier dedupe)", n)
	}
	if prev != 1 {
		t.Fatalf("prev chained %d times, want 1", prev)
	}
}

func TestFindingDrain_SweepBound(t *testing.T) {
	var buf bytes.Buffer
	drain := newFindingDrain(events.New(&buf), nil)
	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	paths := make([]string, 0, 70)
	for i := 0; i < 70; i++ {
		paths = append(paths, "f.go")
	}
	drain.sweep(ws, paths)
	if !strings.Contains(buf.String(), "sweep-skipped") {
		t.Fatalf("bound must announce itself: %s", buf.String())
	}
}

func TestEmitFinding_RoundTrips(t *testing.T) {
	var buf bytes.Buffer
	EmitFinding(events.New(&buf), "per-edit", "gofmt", "a.go", 3, "not gofmt-clean")
	var got []Event
	w := &callbackWriter{onEvent: func(ev Event) { got = append(got, ev) }}
	if _, err := w.Write(buf.Bytes()); err != nil {
		t.Fatalf("write: %v", err)
	}
	if len(got) != 1 || got[0].Name != "finding" {
		t.Fatalf("events = %+v", got)
	}
	fields := got[0].Fields
	if fields["scope"] != "per-edit" || fields["rule"] != "gofmt" || fields["line"] != float64(3) {
		t.Fatalf("fields = %v", fields)
	}
	EmitFinding(nil, "x", "y", "z", 0, "w") // nil-safe
}

func TestEmitNudge_RoundTrips(t *testing.T) {
	var buf bytes.Buffer
	EmitNudge(events.New(&buf), "verify", "[harness] check your work")
	var got []Event
	w := &callbackWriter{onEvent: func(ev Event) { got = append(got, ev) }}
	if _, err := w.Write(buf.Bytes()); err != nil {
		t.Fatalf("write: %v", err)
	}
	if len(got) != 1 || got[0].Name != "nudge" {
		t.Fatalf("events = %+v", got)
	}
	if got[0].Fields["kind"] != "verify" {
		t.Fatalf("fields = %v", got[0].Fields)
	}
	EmitNudge(nil, "x", "y") // nil-safe
}

func TestEmitUsage_RoundTrips(t *testing.T) {
	var buf bytes.Buffer
	EmitUsage(events.New(&buf), 4, llm.Usage{PromptTokens: 12400, CompletionTokens: 300, CachedTokens: 2000})

	var got []Event
	w := &callbackWriter{onEvent: func(ev Event) { got = append(got, ev) }}
	if _, err := w.Write(buf.Bytes()); err != nil {
		t.Fatalf("write: %v", err)
	}
	if len(got) != 1 || got[0].Name != "usage" {
		t.Fatalf("events = %+v", got)
	}
	fields := got[0].Fields
	if fields["step"] != float64(4) || fields["prompt"] != float64(12400) ||
		fields["completion"] != float64(300) || fields["cached"] != float64(2000) {
		t.Fatalf("fields = %+v", fields)
	}
}
