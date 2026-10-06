package api

import (
	"context"
	"testing"
	"time"

	"github.com/keshon/tars/internal/agent"
)

func TestGateHub_SingleFlight(t *testing.T) {
	hub := NewGateHub(nil)
	got := make(chan agent.SuspendReply, 1)
	go func() {
		rep, err := hub.Suspender()(context.Background(), agent.SuspendRequest{Kind: agent.SuspendAsk})
		if err != nil {
			return
		}
		got <- rep
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !hub.HasPending() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !hub.HasPending() {
		t.Fatal("suspender never parked")
	}
	// A second suspension while one is open is a bug, not a queue.
	if _, err := hub.Suspender()(context.Background(), agent.SuspendRequest{}); err == nil {
		t.Fatal("expected double-suspend error")
	}
	// No gate pending without a suspender waiting.
	fresh := NewGateHub(nil)
	if err := fresh.Respond("x"); err == nil {
		t.Fatal("expected no-gate-pending error")
	}
	if err := hub.Respond("answer"); err != nil {
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
	if hub.HasPending() {
		t.Fatal("gate still pending after answer")
	}
}

func TestGateHub_EmitsPrompt(t *testing.T) {
	var names []string
	var prompts []string
	hub := NewGateHub(func(name string, fields map[string]any) {
		names = append(names, name)
		if p, ok := fields["prompt"].(string); ok {
			prompts = append(prompts, p)
		}
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = hub.Suspender()(context.Background(), agent.SuspendRequest{
			Kind:   agent.SuspendAsk,
			Prompt: "which?",
		})
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !hub.HasPending() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if err := hub.Respond("assumed"); err != nil {
		t.Fatal(err)
	}
	<-done
	if len(names) != 2 || names[0] != "awaiting_input" || names[1] != "input_answered" {
		t.Fatalf("events = %v", names)
	}
	// Both events carry the prompt so a consumer can render the question
	// without keeping gate state.
	if len(prompts) != 2 || prompts[0] != "which?" || prompts[1] != "which?" {
		t.Fatalf("prompts = %v", prompts)
	}
}

func TestGateHub_EmitsToolResource(t *testing.T) {
	var got map[string]any
	hub := NewGateHub(func(name string, fields map[string]any) {
		if name == "awaiting_input" {
			got = fields
		}
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = hub.Suspender()(context.Background(), agent.SuspendRequest{
			Kind:     agent.SuspendPermission,
			Tool:     "run_shell",
			Resource: "rm -rf /",
			Prompt:   "allow?",
		})
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !hub.HasPending() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if err := hub.Respond("n"); err != nil {
		t.Fatal(err)
	}
	<-done
	if got["tool"] != "run_shell" || got["resource"] != "rm -rf /" || got["prompt"] != "allow?" {
		t.Fatalf("fields = %v", got)
	}
}

func TestGateCloseReleasesPendingAndRejectsFuture(t *testing.T) {
	ready := make(chan struct{})
	hub := NewGateHub(func(name string, _ map[string]any) {
		if name == "awaiting_input" {
			close(ready)
		}
	})
	done := make(chan error, 1)
	go func() { _, err := hub.Suspender()(context.Background(), agent.SuspendRequest{}); done <- err }()
	<-ready
	hub.Close()
	hub.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("closed input returned a valid answer")
		}
	case <-time.After(time.Second):
		t.Fatal("close stranded gate")
	}
	if _, err := hub.Suspender()(context.Background(), agent.SuspendRequest{}); err == nil {
		t.Fatal("future gate allowed")
	}
}

func TestGateClosePreservesSubmittedAnswer(t *testing.T) {
	ready := make(chan struct{})
	hub := NewGateHub(func(name string, _ map[string]any) {
		if name == "awaiting_input" {
			close(ready)
		}
	})
	done := make(chan agent.SuspendReply, 1)
	fail := make(chan error, 1)
	go func() {
		rep, err := hub.Suspender()(context.Background(), agent.SuspendRequest{})
		if err != nil {
			fail <- err
		} else {
			done <- rep
		}
	}()
	<-ready
	if err := hub.Respond("accepted"); err != nil {
		t.Fatal(err)
	}
	hub.Close()
	select {
	case rep := <-done:
		if rep.Answer != "accepted" {
			t.Fatal(rep)
		}
	case err := <-fail:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("answer lost on close")
	}
}
