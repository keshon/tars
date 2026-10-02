package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// APIError is a backend HTTP failure with its status. RetryAfter, when
// positive, is what the backend asked us to wait (parsed from Retry-After).
// Overflow marks "context window exceeded" responses, which must compact,
// not retry.
type APIError struct {
	Status     int
	StatusText string
	Body       string
	RetryAfter time.Duration
	Overflow   bool
}

func (e *APIError) Error() string {
	if e.Body != "" {
		return fmt.Sprintf("backend returned %d %s — backend said: %s", e.Status, e.StatusText, e.Body)
	}
	return fmt.Sprintf("backend returned %d %s", e.Status, e.StatusText)
}

// Retryable reports whether this failure is worth retrying: 429 and 5xx,
// never overflow (retrying a full context fills nothing) and never 4xx
// otherwise.
func (e *APIError) Retryable() bool {
	if e.Overflow {
		return false
	}
	return e.Status == http.StatusTooManyRequests || e.Status >= 500
}

// AsAPIError unwraps err to an *APIError, if it is one.
func AsAPIError(err error) (*APIError, bool) {
	var api *APIError
	if errors.As(err, &api) {
		return api, true
	}
	return nil, false
}

// IsAbort reports context cancellation by the operator.
func IsAbort(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// IsOverflow reports a context-window-exceeded failure, typed or textual.
func IsOverflow(err error) bool {
	if api, ok := AsAPIError(err); ok && api.Overflow {
		return true
	}
	return isOverflowText(err)
}

func isOverflowText(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, m := range []string{
		"context window", "context length", "context_length",
		"maximum context", "too many tokens", "token limit",
		"ctx size", "n_ctx",
	} {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// IsRetryableTransport reports temporary network failures worth retrying.
func IsRetryableTransport(err error) bool {
	if err == nil || IsAbort(err) {
		return false
	}
	if _, ok := AsAPIError(err); ok {
		return false // API errors judge themselves
	}
	s := strings.ToLower(err.Error())
	for _, m := range []string{
		"connection reset", "connection refused", "broken pipe",
		"timeout", "timed out", "temporary", "try again",
		"service unavailable", "overloaded", "too many requests", "429",
		// Found by a live run: the backend dropped the connection mid-step
		// and the run died on it. Windows words this "forcibly closed by
		// the remote host", Unix "reset by peer" — both are the same
		// transient transport failure and both must retry.
		"forcibly closed", "wsarecv", "reset by peer", "unexpected eof",
		"eof", "server closed", "connection closed",
	} {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// parseRetryAfterMs reads milliseconds from a retry-after-ms value.
func parseRetryAfterMs(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	var ms int
	if _, err := fmt.Sscanf(v, "%d", &ms); err == nil && ms > 0 {
		if ms > 300000 {
			ms = 300000
		}
		return time.Duration(ms) * time.Millisecond
	}
	return 0
}

// parseRetryAfter reads seconds or HTTP-date from a Retry-After value.
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	var secs int
	if _, err := fmt.Sscanf(v, "%d", &secs); err == nil && secs > 0 {
		if secs > 300 {
			secs = 300
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}
