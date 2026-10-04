// Package agent contains the model-agnostic, backend-agnostic agent loop:
// talk to an llm.Client, run whatever tools it asks for, and decide when to
// stop or change strategy. It has no idea koboldcpp or filesystem tools
// exist — those live in internal/llm and internal/tools.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/keshon/tars/internal/llm"
)

// ToolMode tells the loop whether a tool is safe to run concurrently with
// other tool calls in the same step, or must run alone. A purely
// type-level signal can't know whether two write_file calls in one step
// touch the same path or different ones — rather than inspect arguments
// to find out, every Exclusive tool just runs strictly alone. Simpler,
// and the cost (occasionally serializing two writes to different files)
// is small next to the alternative: a write racing a read of the same
// path, or two patches racing each other.
type ToolMode int

const (
	// Concurrent tools are safe to run alongside anything else in the
	// same step: pure reads, independent delegated work, fire-and-forget
	// process management.
	Concurrent ToolMode = iota
	// Exclusive tools mutate the workspace, or do something unanalyzable
	// (an arbitrary shell command) — they run before anything else in
	// the step starts, never overlapping with it.
	Exclusive
)

func (m ToolMode) String() string {
	switch m {
	case Concurrent:
		return "Concurrent"
	case Exclusive:
		return "Exclusive"
	default:
		return "unknown"
	}
}

// Tool is anything the agent can call by name with JSON arguments.
type Tool interface {
	Name() string
	Description() string
	Schema() json.RawMessage // JSON schema for the arguments object
	Mode() ToolMode
	Run(ctx context.Context, args json.RawMessage) (string, error)
}

// Registry is a fixed set of tools, looked up by name.
type Registry struct {
	tools map[string]Tool
}

func NewRegistry(tools ...Tool) *Registry {
	r := &Registry{tools: make(map[string]Tool, len(tools))}
	for _, t := range tools {
		r.tools[t.Name()] = t
	}
	return r
}

// Names returns this registry's tool names, sorted. What an agent may do
// is worth asserting on rather than assuming — see internal/roles, where
// a hand-assembled agent silently ended up with a smaller tool set than
// the one that ships.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Defs returns the tool definitions to hand to the LLM client.
func (r *Registry) Defs() []llm.ToolDef {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)

	defs := make([]llm.ToolDef, 0, len(names))
	for _, name := range names {
		t := r.tools[name]
		defs = append(defs, llm.ToolDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.Schema(),
		})
	}
	return defs
}

func (r *Registry) Run(ctx context.Context, name string, args json.RawMessage) (string, error) {
	t, ok := r.tools[name]
	if !ok {
		return "", fmt.Errorf("unknown tool %q", name)
	}
	return t.Run(ctx, args)
}

// ModeOf reports a tool's concurrency mode. An unknown name (shouldn't
// happen — the model can only call tools it was offered) is treated as
// Exclusive: the safe default when in doubt.
func (r *Registry) ModeOf(name string) ToolMode {
	if t, ok := r.tools[name]; ok {
		return t.Mode()
	}
	return Exclusive
}

// IdempotentOf reports whether a tool has declared (via an optional
// `Idempotent() bool` method) that identical calls return identical
// results as long as nothing has mutated the workspace in between —
// true for pure reads (read_file, list_files, grep_files), never for
// tools whose results vary over time (check_url, check_background) or
// that have side effects (delegate_task). The loop uses this to
// short-circuit exact-repeat calls: a weak model re-issuing the same
// read five times in a row gets told it's repeating instead of
// re-filling its context with the same bytes. Default is false — a tool
// must opt in.
func (r *Registry) IdempotentOf(name string) bool {
	t, ok := r.tools[name]
	if !ok {
		return false
	}
	if it, ok := t.(interface{ Idempotent() bool }); ok {
		return it.Idempotent()
	}
	return false
}

// TodoProgress reports the "todo" tool's checklist state: done count and
// still-open item texts. Zeros when no such tool is registered or it
// doesn't expose progress — the finish gate reads "no list" as "nothing
// pending" (fail-open: registries without a checklist run exactly as
// before). Nil-receiver safe: some loop paths run tool-less.
func (r *Registry) TodoProgress() (done int, open []string) {
	if r == nil {
		return 0, nil
	}
	t, ok := r.tools["todo"]
	if !ok {
		return 0, nil
	}
	if tl, ok := t.(interface{ Progress() (int, []string) }); ok {
		return tl.Progress()
	}
	return 0, nil
}
