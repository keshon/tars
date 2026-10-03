package api

import (
	"context"
	"fmt"
	"sync"

	"github.com/keshon/tars/internal/agent"
)

// GateHub routes one suspension at a time from the run goroutine to
// whoever answers gates: the stdio server dispatcher, the TUI event
// loop, or a test. Single-flight is asserted, not assumed: a second
// suspension or answer while one is open reports an error instead of
// deadlocking or queuing.
type GateHub struct {
	mu      sync.Mutex
	pending bool
	replyCh chan agent.SuspendReply
	emit    func(name string, fields map[string]any)
}

// NewGateHub builds a hub; emit carries awaiting_input/input_answered
// events (including the prompt text the operator must see) and may be nil.
func NewGateHub(emit func(name string, fields map[string]any)) *GateHub {
	return &GateHub{emit: emit}
}

func (h *GateHub) event(name string, req agent.SuspendRequest) {
	if h.emit == nil {
		return
	}
	// Tool/resource ride along so permission UIs can name the gated
	// call (and confirm always-scopes) without keeping gate state.
	// Ask gates leave them empty. Additive: old consumers ignore them.
	h.emit(name, map[string]any{
		"kind":     string(req.Kind),
		"prompt":   req.Prompt,
		"tool":     req.Tool,
		"resource": req.Resource,
	})
}

// Suspender blocks until Respond answers, the context dies, or another
// suspension is already open.
func (h *GateHub) Suspender() agent.Suspender {
	return func(ctx context.Context, req agent.SuspendRequest) (agent.SuspendReply, error) {
		h.mu.Lock()
		if h.pending {
			h.mu.Unlock()
			return agent.SuspendReply{}, fmt.Errorf("gate already pending")
		}
		h.pending = true
		ch := make(chan agent.SuspendReply, 1)
		h.replyCh = ch
		h.mu.Unlock()
		h.event("awaiting_input", req)
		select {
		case <-ctx.Done():
			h.clear()
			return agent.SuspendReply{}, ctx.Err()
		case rep := <-ch:
			h.clear()
			h.event("input_answered", req)
			return rep, nil
		}
	}
}

func (h *GateHub) clear() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pending = false
}

// Respond answers the pending gate. Raw text in, like the terminal: the
// y/a/n and approve/reject mappings live in the gate wrappers, not here
// and not in any transport.
func (h *GateHub) Respond(answer string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.pending {
		return fmt.Errorf("no gate pending")
	}
	h.pending = false
	h.replyCh <- agent.SuspendReply{Answer: answer}
	return nil
}

// HasPending reports whether a gate is suspended awaiting an answer.
// Shutdown uses it: a pending gate with a closed transport can never be
// answered, so the run is cancelled instead of drained.
func (h *GateHub) HasPending() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pending
}
