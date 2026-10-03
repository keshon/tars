package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keshon/tars/internal/agent"
	"github.com/keshon/tars/internal/llm"
	"github.com/keshon/tars/internal/tools"
	"github.com/keshon/tars/internal/workspace"
)

type stubChat struct {
	mu        sync.Mutex
	responses []llm.ChatResponse
	calls     int
}

func (s *stubChat) Chat(_ context.Context, _ llm.ChatRequest) (llm.ChatResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.responses[s.calls%len(s.responses)]
	s.calls++
	return r, nil
}

func testServeDeps(t *testing.T, client llm.Client) serveDeps {
	t.Helper()
	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return serveDeps{
		client: client, ws: ws, procs: tools.NewBackgroundProcesses(),
	}
}

// outLines collects stdout lines from a serveMain under test.
type outLines struct {
	mu    sync.Mutex
	lines []string
	ch    chan string
}

func (o *outLines) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, line := range strings.Split(string(p), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		o.lines = append(o.lines, line)
		select {
		case o.ch <- line:
		default:
		}
	}
	return len(p), nil
}

func (o *outLines) waitFor(t *testing.T, timeout time.Duration, match func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		o.mu.Lock()
		lines := append([]string(nil), o.lines...)
		o.mu.Unlock()
		for _, line := range lines {
			var rec map[string]any
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				continue
			}
			if match(rec) {
				return rec
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for output; got %d lines: %v", len(lines), lines)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func runServe(t *testing.T, deps serveDeps, script []string) *outLines {
	t.Helper()
	inR, inW := io.Pipe()
	out := &outLines{ch: make(chan string, 256)}
	var notes bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		serveMain(context.Background(), deps, inR, out, &notes)
	}()
	t.Cleanup(func() {
		_ = inW.Close()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("serveMain did not exit on stdin EOF")
		}
	})
	for _, line := range script {
		if _, err := io.WriteString(inW, line+"\n"); err != nil {
			t.Fatalf("write request: %v", err)
		}
	}
	return out
}

func TestServe_RunEndToEnd(t *testing.T) {
	stub := &stubChat{responses: []llm.ChatResponse{
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}},
	}}
	out := runServe(t, testServeDeps(t, stub),
		[]string{`{"id":1,"method":"run","params":{"task":"hi"}}`})

	rec := out.waitFor(t, 10*time.Second, func(rec map[string]any) bool {
		id, _ := rec["id"].(float64)
		_, hasResult := rec["result"]
		return id == 1 && hasResult
	})
	result, _ := rec["result"].(map[string]any)
	if result["answer"] != "done" {
		t.Fatalf("result = %+v", rec)
	}
	// The run announced itself before answering.
	out.waitFor(t, time.Second, func(rec map[string]any) bool {
		ev, _ := rec["event"].(string)
		return ev == "run_start"
	})
}

func TestServe_RespondAnswersGate(t *testing.T) {
	stub := &stubChat{responses: []llm.ChatResponse{
		{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			{ID: "q1", Name: "ask_user", Arguments: json.RawMessage(`{"question":"which?"}`)},
		}}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}},
		// Twice: the verify round asks the model once more before
		// accepting the finish.
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}},
	}}
	inR, inW := io.Pipe()
	out := &outLines{ch: make(chan string, 256)}
	var notes bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		serveMain(context.Background(), testServeDeps(t, stub), inR, out, &notes)
	}()
	defer func() {
		_ = inW.Close()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("serveMain did not exit on stdin EOF")
		}
	}()

	if _, err := io.WriteString(inW, "{\"id\":1,\"method\":\"run\",\"params\":{\"task\":\"hi\"}}\n"); err != nil {
		t.Fatal(err)
	}
	// The run must suspend on the question instead of touching stdin.
	out.waitFor(t, 10*time.Second, func(rec map[string]any) bool {
		ev, _ := rec["event"].(string)
		return ev == "awaiting_input"
	})
	if _, err := io.WriteString(inW, "{\"id\":2,\"method\":\"respond\",\"params\":{\"answer\":\"assumed\"}}\n"); err != nil {
		t.Fatal(err)
	}
	rec := out.waitFor(t, 10*time.Second, func(rec map[string]any) bool {
		id, _ := rec["id"].(float64)
		_, hasResult := rec["result"]
		return id == 1 && hasResult
	})
	if rec["result"].(map[string]any)["answer"] != "done" {
		t.Fatalf("result = %+v", rec)
	}
	// And the respond itself was acknowledged.
	out.waitFor(t, time.Second, func(rec map[string]any) bool {
		id, _ := rec["id"].(float64)
		_, hasResult := rec["result"]
		return id == 2 && hasResult
	})
}

func TestServe_RejectsSecondRun(t *testing.T) {
	hanging := &hangForever{}
	inR, inW := io.Pipe()
	out := &outLines{ch: make(chan string, 256)}
	var notes bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		serveMain(context.Background(), testServeDeps(t, hanging), inR, out, &notes)
	}()
	defer func() {
		_ = inW.Close()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("serveMain did not exit on stdin EOF")
		}
	}()

	if _, err := io.WriteString(inW, "{\"id\":1,\"method\":\"run\",\"params\":{\"task\":\"slow\"}}\n"); err != nil {
		t.Fatal(err)
	}
	// Give the first run a moment to start, then a second run must be
	// refused — single-flight, never queued.
	deadline := time.Now().Add(5 * time.Second)
	for {
		out.mu.Lock()
		n := len(out.lines)
		out.mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := io.WriteString(inW, "{\"id\":2,\"method\":\"run\",\"params\":{\"task\":\"other\"}}\n"); err != nil {
		t.Fatal(err)
	}
	rec := out.waitFor(t, 10*time.Second, func(rec map[string]any) bool {
		id, _ := rec["id"].(float64)
		_, hasErr := rec["error"]
		return id == 2 && hasErr
	})
	if !strings.Contains(rec["error"].(map[string]any)["message"].(string), "already in progress") {
		t.Fatalf("error = %+v", rec)
	}
	// Cancel the hung run so the server can shut down cleanly.
	if _, err := io.WriteString(inW, "{\"id\":3,\"method\":\"cancel\"}\n"); err != nil {
		t.Fatal(err)
	}
	out.waitFor(t, 10*time.Second, func(rec map[string]any) bool {
		id, _ := rec["id"].(float64)
		_, hasResult := rec["result"]
		return id == 3 && hasResult
	})
}

type hangForever struct{}

func (hangForever) Chat(ctx context.Context, _ llm.ChatRequest) (llm.ChatResponse, error) {
	<-ctx.Done()
	return llm.ChatResponse{}, ctx.Err()
}

func TestGateHub_SingleFlight(t *testing.T) {
	hub := newGateHub(nil)
	got := make(chan agent.SuspendReply, 1)
	go func() {
		rep, err := hub.suspender()(context.Background(), agent.SuspendRequest{Kind: agent.SuspendAsk})
		if err != nil {
			return
		}
		got <- rep
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		hub.mu.Lock()
		pending := hub.pending
		hub.mu.Unlock()
		if pending || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	// A second suspension while one is open is a bug, not a queue.
	if _, err := hub.suspender()(context.Background(), agent.SuspendRequest{}); err == nil {
		t.Fatal("expected double-suspend error")
	}
	// No gate pending without a suspender waiting.
	fresh := newGateHub(nil)
	if err := fresh.respond("x"); err == nil {
		t.Fatal("expected no-gate-pending error")
	}
	if err := hub.respond("answer"); err != nil {
		t.Fatalf("respond: %v", err)
	}
	select {
	case rep := <-got:
		if rep.Answer != "answer" {
			t.Fatalf("reply = %+v", rep)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("suspender never woke")
	}
}
