package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStream_DeltasAndAssembly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "text/event-stream" {
			t.Errorf("Accept = %q, want SSE", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, line := range []string{
			`data: {"choices":[{"delta":{"content":"hel"}}]}`,
			`data: {"choices":[{"delta":{"content":"lo"}}]}`,
			`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			`data: [DONE]`,
		} {
			w.Write([]byte(line + "\n\n"))
		}
	}))
	defer srv.Close()

	c := NewLlamaClient(srv.URL, "test")
	var deltas []string
	resp, err := c.Stream(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, func(s string) { deltas = append(deltas, s) })
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if strings.Join(deltas, "") != "hello" {
		t.Fatalf("deltas = %q", deltas)
	}
	if resp.Message.Content != "hello" {
		t.Fatalf("assembled = %q", resp.Message.Content)
	}
}

func TestStream_KoboldRefuses(t *testing.T) {
	// Refusal happens before any HTTP dial: unreachable URL proves it.
	c := NewKoboldClient("http://127.0.0.1:1", "x")
	called := false
	_, err := c.Stream(context.Background(), ChatRequest{}, func(string) { called = true })
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "unsupported") {
		t.Fatalf("err = %v, want unsupported", err)
	}
	if called {
		t.Fatal("no deltas may flow from a refused stream")
	}
}

func TestStream_EstimatesUsageWhenAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello there, \"}}]}\n\n"))
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"how are you doing today my friend\"}}]}\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	c := NewLlamaClient(srv.URL, "test")
	resp, err := c.Stream(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hello there, how are you doing today my friend"}},
	}, func(string) {})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if !resp.Usage.Estimated {
		t.Fatal("usage without a block must flag estimated")
	}
	if resp.Usage.PromptTokens <= 0 || resp.Usage.CompletionTokens <= 0 {
		t.Fatalf("usage = %+v, want positive estimates", resp.Usage)
	}
}

func TestStream_KeepsMeasuredUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		w.Write([]byte("data: {\"usage\":{\"prompt_tokens\":2100,\"completion_tokens\":120}}\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	c := NewLlamaClient(srv.URL, "test")
	resp, err := c.Stream(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, func(string) {})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if resp.Usage.Estimated {
		t.Fatal("measured usage must not flag estimated")
	}
	if resp.Usage.PromptTokens != 2100 || resp.Usage.CompletionTokens != 120 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
}
