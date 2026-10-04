package agent

import (
	"context"
	"fmt"
)

// SuspendKind names what a run is waiting on. The loop itself never
// blocks on a human: tools and gates ask through a Suspender, and the
// implementation decides whether that means stdin (today), a channel
// (a future TUI or RPC server), or an error (headless runs).
type SuspendKind string

const (
	// SuspendAsk is a clarifying question from ask_user.
	SuspendAsk SuspendKind = "ask_user"
	// SuspendPermission is an Ask-gated tool call awaiting allow/deny.
	SuspendPermission SuspendKind = "permission"
	// SuspendPlan is a mission plan awaiting approve/reject/note.
	SuspendPlan SuspendKind = "plan_approval"
	// SuspendBudget is an exhausted step budget with acknowledged work
	// unfinished, asking the operator for one more base budget. Deny,
	// error, or headless all decline — the run fails as before.
	SuspendBudget SuspendKind = "budget"
)

// SuspendRequest is one blocked gate.
type SuspendRequest struct {
	Kind SuspendKind
	// ID correlates the reply: the tool call ID for ask/permission, a
	// gate ID for plan approval.
	ID string
	// Prompt is what the operator must see and react to: the question,
	// the rendered plan, or the permission text.
	Prompt string
	// Tool and Resource further describe a permission gate.
	Tool     string
	Resource string
}

// SuspendReply answers one blocked gate. Only the fields matching the
// request kind matter: Answer for ask_user and plan notes, Allow/Always
// for permission and plan approval.
type SuspendReply struct {
	Answer string
	Allow  bool
	Always bool
}

// Suspender answers one blocked gate. Calling it may block as long as
// the transport needs — stdin, a channel, a socket — or fail immediately
// when nobody is there to answer.
type Suspender func(ctx context.Context, req SuspendRequest) (SuspendReply, error)

// Closed returns a Suspender that always fails: for runs with no
// operator, where blocking on stdin would hang until the wall clock
// kills the process. Fail-closed, never silent.
func ClosedSuspender(reason string) Suspender {
	return func(_ context.Context, req SuspendRequest) (SuspendReply, error) {
		return SuspendReply{}, fmt.Errorf("no operator to answer %s: %s", req.Kind, reason)
	}
}
