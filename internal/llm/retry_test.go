package llm

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

type flakyClient struct {
	failures int
	calls    int
	err      error
}

func (f *flakyClient) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	f.calls++
	if f.calls <= f.failures {
		return ChatResponse{}, f.err
	}
	return ChatResponse{Message: Message{Role: RoleAssistant, Content: "ok"}}, nil
}

func TestChatWithRetry_RetriesThenSucceeds(t *testing.T) {
	c := &flakyClient{failures: 2, err: &APIError{Status: 503, StatusText: "unavailable"}}
	policy := RetryPolicy{MaxRetries: 3, Initial: time.Millisecond, MaxDelay: 5 * time.Millisecond}
	resp, err := ChatWithRetry(context.Background(), c, ChatRequest{}, policy)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp.Message.Content != "ok" || c.calls != 3 {
		t.Fatalf("calls=%d content=%q", c.calls, resp.Message.Content)
	}
}

func TestChatWithRetry_NoRetryOn400(t *testing.T) {
	c := &flakyClient{failures: 5, err: &APIError{Status: 400, StatusText: "bad"}}
	policy := RetryPolicy{MaxRetries: 3, Initial: time.Millisecond, MaxDelay: 5 * time.Millisecond}
	if _, err := ChatWithRetry(context.Background(), c, ChatRequest{}, policy); err == nil {
		t.Fatal("expected error")
	}
	if c.calls != 1 {
		t.Fatalf("calls=%d, want 1", c.calls)
	}
}

func TestChatWithRetry_NoRetryOnOverflow(t *testing.T) {
	c := &flakyClient{failures: 5, err: &APIError{Status: 400, StatusText: "bad", Overflow: true}}
	policy := RetryPolicy{MaxRetries: 3, Initial: time.Millisecond, MaxDelay: 5 * time.Millisecond}
	_, err := ChatWithRetry(context.Background(), c, ChatRequest{}, policy)
	if !IsOverflow(err) {
		t.Fatalf("expected overflow, got %v", err)
	}
	if c.calls != 1 {
		t.Fatalf("calls=%d, want 1", c.calls)
	}
}

func TestChatWithRetry_AbortSurfaces(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ChatWithRetry(ctx, &flakyClient{}, ChatRequest{}, RetryPolicy{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancel, got %v", err)
	}
}

func TestAPIError_Retryable(t *testing.T) {
	if !(&APIError{Status: 429}).Retryable() {
		t.Error("429 must retry")
	}
	if (&APIError{Status: 400}).Retryable() {
		t.Error("400 must not retry")
	}
	if (&APIError{Status: 500, Overflow: true}).Retryable() {
		t.Error("overflow must not retry")
	}
}

func TestIsOverflowText(t *testing.T) {
	if !IsOverflow(fmt.Errorf("this exceeds the context window")) {
		t.Error("expected overflow")
	}
	if IsOverflow(fmt.Errorf("backend returned 500")) {
		t.Error("500 is not overflow")
	}
}

// A live kobold run died on this exact string; a dropped connection must
// retry, not end the mission.
func TestIsRetryableTransport_DroppedConnection(t *testing.T) {
	for _, msg := range []string{
		`Post "http://127.0.0.1:5001/v1/chat/completions": read tcp 127.0.0.1:51279->127.0.0.1:5001: wsarecv: An existing connection was forcibly closed by the remote host.`,
		"read: connection reset by peer",
		"unexpected EOF",
	} {
		if !IsRetryableTransport(fmt.Errorf("%s", msg)) {
			t.Errorf("want retry: %s", msg)
		}
	}
	if IsRetryableTransport(fmt.Errorf("decode response: invalid character")) {
		t.Error("decode errors must not retry")
	}
	if IsRetryableTransport(context.Canceled) {
		t.Error("cancel must not retry")
	}
}

func TestServer_UsageTotalsAccumulate(t *testing.T) {
	s := &Server{}
	if p, g, n := s.UsageTotals(); p != 0 || g != 0 || n != 0 {
		t.Fatalf("fresh totals = %d/%d/%d, want 0/0/0", p, g, n)
	}
	s.recordUsage(100, 20)
	s.recordUsage(50, 5)
	if p, g, n := s.UsageTotals(); p != 150 || g != 25 || n != 2 {
		t.Fatalf("totals = %d/%d/%d, want 150/25/2", p, g, n)
	}
}
