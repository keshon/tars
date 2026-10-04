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
	"strings"
	"sync"

	"github.com/keshon/tars/internal/llm"
	"github.com/keshon/tars/internal/permission"
	"github.com/keshon/tars/internal/prompts"
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

// maxIdenticalAttempts is how many identical attempts at a call are
// allowed while it keeps failing and nothing mutates in between: the
// call executes twice and the third attempt is refused without running.
// Above two so a genuinely time-dependent retry has room — polling a
// server that is still starting is the honest case, and start_background
// is Concurrent so it does not clear this cache.
const maxIdenticalAttempts = 3

// executeToolCalls runs one step's tool calls and accounts for them:
// repeat detection, refusal short-circuits, policy gates, concurrent vs
// exclusive scheduling, result collection, mutation bookkeeping, loop
// counters, and step funding. It returns whether the calls repeated the
// previous step and the grown history — it never finishes runs; the
// loop decides that from the returned repeat flag.
func (a *Agent) executeToolCalls(ctx context.Context, st *runState, step int, calls []llm.ToolCall, history []llm.Message) (bool, []llm.Message) {
	signature := callSignature(calls)
	repeat := signature == st.lastSignature
	st.lastSignature = signature
	// Acting resets the todo-bounce latch: only back-to-back
	// narration without intervening work counts toward it.
	st.todoBounces = 0

	// Tool calls within one step are scheduled by Tool.Mode(), not run
	// uniformly. Concurrent calls (reads, independent delegate_task
	// calls, read-only checks) run together via goroutines — this is
	// what makes multiple delegate_task calls in one step actually
	// run in parallel. Exclusive calls (anything that mutates the
	// workspace, or run_shell's unanalyzable arbitrary command) run
	// one at a time and never overlap with the Concurrent batch or
	// each other — a model issuing write_file then patch_file on the
	// same file in one step can rely on that order; two reads can't
	// race a write of the same path either way, in any order.
	results := make([]callResult, len(calls))

	// Exact repeats of idempotent (pure-read) calls don't re-execute:
	// nothing has changed, so the result would be byte-identical — and
	// re-delivering it teaches a weak model nothing while filling the
	// context with duplicates. The repeat comes back as an *error*
	// result on purpose: errors don't count as progress, so the stuck
	// detector keeps escalating if the model won't change course.
	//
	// write_file to a path already written this run is refused the same
	// way: soft nudges don't break rewrite loops when every call
	// returns success (progressed=true clears stuckSteps).
	skipped := make([]bool, len(calls))
	for i, call := range calls {
		if call.Name == "write_file" {
			if path := writePathFromArgs(call.Arguments); path != "" {
				if prev, seen := st.wrotePaths[path]; seen {
					skipped[i] = true
					results[i] = callResult{err: fmt.Errorf(
						"already wrote %s in step %d — it is on disk. Do NOT rewrite it with "+
							"write_file. Use patch_file for edits, or write_file the NEXT missing "+
							"file from your files_hint / acceptance list, then finish when done",
						path, prev)}
					continue
				}
			}
		}
		// A call that already failed, repeated identically with nothing
		// mutated since, fails identically again. Soft nudges do not
		// stop this: run_shell already returns an error on a non-zero
		// exit, so the stuck counter was climbing and escalating the
		// whole time a live worker ran `go test <one _test.go file>`
		// seven times. It ignored every nudge. A refusal is not
		// ignorable.
		//
		// The tolerance exists because a few tools are legitimately
		// time-dependent — check_url against a server that is still
		// starting is the honest case, and start_background is
		// Concurrent so it does not clear this cache.
		key := callKey(call.Name, call.Arguments)
		if n := st.failedCalls[key]; n >= maxIdenticalAttempts-1 {
			skipped[i] = true
			results[i] = callResult{err: fmt.Errorf(
				"this exact %s call has already failed %d times and nothing in the workspace "+
					"has changed since — it will fail the same way again. Read the error above "+
					"and fix the cause, or take a different approach; do not re-run it",
				call.Name, n)}
			continue
		}

		if !a.cfg.Tools.IdempotentOf(call.Name) {
			continue
		}
		sig := key
		if prev, seen := st.idempotentSeen[sig]; seen {
			skipped[i] = true
			results[i] = callResult{err: fmt.Errorf(
				"this exact %s call (same arguments) already ran in step %d and nothing has "+
					"changed since — its result is still valid, re-read it from the conversation. "+
					"Do not repeat the call; do something different (different path, different "+
					"arguments, or move on to acting on what you already know)",
				call.Name, prev)}
		}
	}

	var concurrentIdx, exclusiveIdx []int
	for i, call := range calls {
		if skipped[i] {
			continue
		}
		if a.cfg.Tools.ModeOf(call.Name) == Concurrent {
			concurrentIdx = append(concurrentIdx, i)
		} else {
			exclusiveIdx = append(exclusiveIdx, i)
		}
	}

	runOne := func(i int) {
		call := calls[i]
		resource := toolResource(call.Name, call.Arguments)
		if a.cfg.BeforeToolCall != nil {
			eff := a.cfg.Policy.Evaluate(call.Name, resource)
			if eff == permission.Ask || eff == permission.Deny {
				hookEff, hookErr := a.cfg.BeforeToolCall(ctx, call.Name, resource, call.Arguments)
				if hookErr != nil {
					results[i] = callResult{err: hookErr}
					if a.cfg.AfterToolCall != nil {
						a.cfg.AfterToolCall(call.Name, resource, "", hookErr)
					}
					return
				}
				eff = hookEff
			}
			if eff == permission.Deny {
				denied := fmt.Errorf("blocked by policy (%s on %s)", call.Name, resource)
				results[i] = callResult{err: denied}
				if a.cfg.AfterToolCall != nil {
					a.cfg.AfterToolCall(call.Name, resource, "", denied)
				}
				return
			}
		}
		content, err := a.cfg.Tools.Run(ctx, call.Name, call.Arguments)
		results[i] = callResult{content: content, err: err}
		if a.cfg.AfterToolCall != nil {
			a.cfg.AfterToolCall(call.Name, resource, content, err)
		}
	}

	if len(concurrentIdx) > 0 {
		var wg sync.WaitGroup
		for _, i := range concurrentIdx {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				runOne(i)
			}(i)
		}
		wg.Wait()
	}
	for _, i := range exclusiveIdx {
		runOne(i)
	}

	mutatingBefore := st.mutatingSucceeded
	progressed := false
	exclusiveSucceeded := false
	for i, call := range calls {
		content := results[i].content
		if results[i].err != nil {
			content = "error: " + results[i].err.Error()
			if !skipped[i] {
				// Skipped calls are already refusals; counting them
				// would let the counter climb without the model ever
				// having re-attempted anything.
				if st.failedCalls == nil {
					st.failedCalls = make(map[string]int)
				}
				st.failedCalls[callKey(call.Name, call.Arguments)]++
			}
		} else {
			progressed = true
			delete(st.failedCalls, callKey(call.Name, call.Arguments))
			if a.cfg.Tools.ModeOf(call.Name) == Exclusive {
				exclusiveSucceeded = true
			}
			if a.cfg.Tools.IdempotentOf(call.Name) {
				if st.idempotentSeen == nil {
					st.idempotentSeen = make(map[string]int)
				}
				st.idempotentSeen[callKey(call.Name, call.Arguments)] = step
			}
			if containsStr(a.cfg.MutatingTools, call.Name) {
				st.mutatingSucceeded++
				for _, p := range mutatedPathsFromCall(call.Arguments) {
					if !containsStr(st.mutatedPaths, p) {
						st.mutatedPaths = append(st.mutatedPaths, p)
					}
				}
				if call.Name == "write_file" {
					if path := writePathFromArgs(call.Arguments); path != "" {
						if st.wrotePaths == nil {
							st.wrotePaths = make(map[string]int)
						}
						st.wrotePaths[path] = step
					}
				}
			}
			if call.Name == "delegate_task" {
				if m, paths := parseDelegateMutations(content); m > 0 {
					st.mutatingSucceeded += m
					for _, p := range paths {
						if !containsStr(st.mutatedPaths, p) {
							st.mutatedPaths = append(st.mutatedPaths, p)
						}
					}
				}
			}
		}
		history = append(history, llm.Message{
			Role:       llm.RoleTool,
			ToolCallID: call.ID,
			Content:    content,
		})
		if a.cfg.OnToolResult != nil {
			a.cfg.OnToolResult(call.ID, content)
		}
	}

	// Anything that mutated (an Exclusive call — write/patch/shell — or
	// a subagent reporting writes) invalidates the repeat cache: the
	// same read can now legitimately return something new, and a call
	// that failed before may now succeed.
	if exclusiveSucceeded || st.mutatingSucceeded > mutatingBefore {
		dropRepeatCaches(st)
	}

	// A step that changed the workspace is progress by definition, so
	// it can never be evidence of a loop. Writing four different files
	// in a row is the normal shape of a scaffolding subtask — it is
	// write_file four times, which is exactly what the same-tool
	// counter used to flag. Live failure, FPS mission: package.json, vite.config.ts and index.html were written
	// correctly, tripped the nudge at three, and the worker was told
	// to "switch to a genuinely different tool" mid-scaffold.
	// src/main.ts — the third acceptance criterion — was never written.
	//
	// Exact repeats are already handled by callSignature/idempotentSeen
	// and wrotePaths, so what's left for the same-tool counter is the
	// narrow case that nudge was written for: grinding one read-only
	// tool with slightly different arguments, getting the same bytes
	// back, and never converging. Fresh results restart the chain —
	// see trackSingleToolLoop.
	mutated := st.mutatingSucceeded > mutatingBefore
	if mutated {
		st.exploratorySteps = 0
		st.lastSingleTool = ""
		st.lastSingleResult = 0
		st.consecutiveSameToolCount = 0
	} else {
		st.exploratorySteps++
		a.trackSingleToolLoop(calls, results, st)
	}
	if a.cfg.TodoFunding {
		fundTodoSteps(a.cfg.Tools, a.cfg.MaxSteps, st)
		if checkTodoClosing(a.cfg.Tools, st) {
			st.todoFunded += todoClosingSteps
			history = a.nudge(history, NudgeClosing, fmt.Sprintf(prompts.Closing, todoClosingSteps))
		}
		a.report.MaxSteps = a.cfg.MaxSteps + st.todoFunded
	}
	if progressed && !repeat {
		st.stuckSteps = 0
	} else {
		st.stuckSteps++
	}
	return repeat, history
}

// callResult is one executed tool call's outcome: either content or err
// is set. It lives beside the execution that produces it (tools.go)
// rather than the loop that consumes it.
type callResult struct {
	content string
	err     error
}

// callSignature identifies a set of tool calls by name+arguments, order
// independent, so the loop can tell "the model issued the exact same
// call(s) again" apart from "the model made progress" - a tool call that
// succeeds without error is not the same thing as the model moving
// forward if it's the same call as last time.
func callSignature(calls []llm.ToolCall) string {
	parts := make([]string, len(calls))
	for i, c := range calls {
		parts[i] = callKey(c.Name, c.Arguments)
	}
	sort.Strings(parts)
	return strings.Join(parts, "|")
}

// callKey identifies one tool call for repeat detection, canonicalizing
// the arguments so that spelling differences don't read as different
// calls. A model re-reading one file emits {"path":"x"} one step and
// {"path": "x", "metadata_only": false} the next; on raw bytes those are
// two distinct keys, so the repeat guard never fires and the same file
// comes back again. Round-tripping through a map sorts the keys and
// drops the whitespace.
//
// Arguments that aren't a JSON object (malformed output, a bare string)
// fall back to the raw bytes: better a key that is too specific than one
// that collapses two genuinely different calls into one.
func callKey(name string, args json.RawMessage) string {
	var obj map[string]any
	if err := json.Unmarshal(args, &obj); err != nil {
		return name + ":" + string(args)
	}
	canonical, err := json.Marshal(obj)
	if err != nil {
		return name + ":" + string(args)
	}
	return name + ":" + string(canonical)
}
