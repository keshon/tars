package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/keshon/tars/internal/agent"
)

// Delegate lets an agent hand off a self-contained subtask to a fresh
// subagent and get back its final answer. This is the entire
// "orchestrator" from agent-v3, replaced by one tool: delegation doesn't
// need its own architecture, just a tool that recursively builds and runs
// another *agent.Agent.
const defaultMaxConcurrentDelegates = 3

type Delegate struct {
	// Spawn builds a fresh subagent for one subtask. role, if non-empty,
	// is layered onto the subagent's base system prompt rather than
	// replacing it — the caller still gets the baseline tool-usage rules
	// (move_file, list_files-before-acting, etc); the subagent just also
	// adopts a specific expertise, behavior, or identity for this task.
	// Spawn typically shares the same Client and Workspace but a smaller,
	// task-specific tool set — in particular, without Delegate itself, so
	// a task can't recurse into subagents forever.
	Spawn func(role string) *agent.Agent

	// MaxConcurrent caps parallel delegate_task calls. Zero means 3.
	MaxConcurrent int

	sem     chan struct{}
	semOnce sync.Once
}

// All methods are pointer receivers: Delegate embeds a sync.Once, and a
// value receiver would copy it (go vet: "passes lock by value").
func (*Delegate) Name() string { return "delegate_task" }

// Delegates share a workspace, so each one runs without overlapping parent tools.
func (*Delegate) Mode() agent.ToolMode { return agent.Exclusive }
func (*Delegate) Description() string {
	return "Delegate a self-contained subtask to a fresh subagent and return its final result. " +
		"Use this for work that can be fully described in one instruction and doesn't need the " +
		"current conversation's context. Optionally set role to specialize the subagent for this " +
		"task — an expertise, a behavior pattern, a strict constraint, or a character/identity " +
		"(e.g. 'a strict security reviewer who rejects anything unvalidated', 'a Python-only " +
		"specialist', or 'Alice, a software engineer who loves tea'). This actually shapes how " +
		"the subagent behaves, not just text embedded in the task description."
}
func (*Delegate) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"task": {"type": "string"},
			"role": {"type": "string"}
		},
		"required": ["task"]
	}`)
}

// semaphore also bounds concurrent callers using Delegate outside the scheduler.
func (d *Delegate) semaphore() chan struct{} {
	d.semOnce.Do(func() {
		capacity := d.MaxConcurrent
		if capacity <= 0 {
			capacity = defaultMaxConcurrentDelegates
		}
		d.sem = make(chan struct{}, capacity)
	})
	return d.sem
}

func (d *Delegate) Run(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Task string `json:"task"`
		Role string `json:"role"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	sem := d.semaphore()
	select {
	case sem <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-sem }()

	sub := d.Spawn(in.Role)
	result, err := sub.Run(ctx, in.Task)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(result) == "" {
		return "", fmt.Errorf("subagent returned empty result")
	}
	return formatDelegateResult(sub.LastRunMutations, sub.MutatedPaths(), result), nil
}

func formatDelegateResult(mutations int, paths []string, result string) string {
	var b strings.Builder
	b.WriteString("DELEGATE\n")
	fmt.Fprintf(&b, "mutations: %d\n", mutations)
	for _, p := range paths {
		fmt.Fprintf(&b, "paths: %s\n", p)
	}
	b.WriteString("----\n")
	b.WriteString(result)
	return b.String()
}
