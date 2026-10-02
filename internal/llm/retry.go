package llm

import (
	"context"
	"fmt"
	"math/rand"
	"time"
)

// RetryPolicy bounds chat retries. Zero values take defaults.
type RetryPolicy struct {
	MaxRetries int           // default 5
	Initial    time.Duration // default 2s
	MaxDelay   time.Duration // default 30s
}

// DefaultRetryPolicy is 5 retries, 2s initial doubling with 25% jitter,
// capped at 30s — matching opencode's session/retry.ts shape.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{MaxRetries: 5, Initial: 2 * time.Second, MaxDelay: 30 * time.Second}
}

func (p RetryPolicy) withDefaults() RetryPolicy {
	if p.MaxRetries <= 0 {
		p.MaxRetries = 5
	}
	if p.Initial <= 0 {
		p.Initial = 2 * time.Second
	}
	if p.MaxDelay <= 0 {
		p.MaxDelay = 30 * time.Second
	}
	return p
}

// ChatWithRetry calls Chat, retrying 429/5xx and transport failures with
// backoff. It honors the backend's Retry-After when larger than the
// computed delay. Aborts and overflows never retry: the first must surface
// immediately, the second needs compaction, not another attempt.
func ChatWithRetry(ctx context.Context, c Client, req ChatRequest, policy RetryPolicy) (ChatResponse, error) {
	policy = policy.withDefaults()
	delay := policy.Initial
	var err error
	var resp ChatResponse
	for attempt := 0; ; attempt++ {
		if ctx.Err() != nil {
			return ChatResponse{}, ctx.Err()
		}
		resp, err = c.Chat(ctx, req)
		if err == nil {
			return resp, nil
		}
		if IsAbort(err) || IsOverflow(err) {
			return ChatResponse{}, err
		}
		retryable := IsRetryableTransport(err)
		var wait time.Duration
		if api, ok := AsAPIError(err); ok {
			if !api.Retryable() {
				return ChatResponse{}, err
			}
			retryable = true
			wait = api.RetryAfter
		}
		if !retryable || attempt >= policy.MaxRetries {
			return ChatResponse{}, err
		}
		if wait <= 0 {
			wait = delay
		}
		if wait > policy.MaxDelay {
			wait = policy.MaxDelay
		}
		// 25% jitter so parallel workers don't march in lockstep.
		jitter := time.Duration(rand.Int63n(int64(wait) / 4))
		wait += jitter
		select {
		case <-ctx.Done():
			return ChatResponse{}, ctx.Err()
		case <-time.After(wait):
		}
		delay *= 2
		if delay > policy.MaxDelay {
			delay = policy.MaxDelay
		}
	}
}

// RetryableError formats the last retry failure for logs.
func RetryableError(attempt int, err error) string {
	return fmt.Sprintf("backend attempt %d failed: %v", attempt+1, err)
}
