// Package roles builds the agents this project runs.
//
// There are only four kinds of agent here, but before this package each
// was configured inline at its call site — seven sites, four of which
// were meant to be identical in pairs. They drifted, and the drift was
// not theoretical: the eval harness built its agent without ask_user and
// delegate_task while the CLI's had both, so the instrument was scoring a
// different agent than the one that ships. Two of the probes are
// specifically about those tools.
//
// The cure is that "what is a subagent allowed to do?" has exactly one
// answer, written down once. Callers still choose the parts that are
// genuinely theirs — which system prompt, where the transcript goes, how
// many steps — and nothing else.
//
// This lives outside internal/agent on purpose: agent defines the Tool
// interface that tools implements, so agent cannot import tools without a
// cycle. roles is the layer that knows about both.
package roles

import (
	"context"
	"encoding/json"
	"path/filepath"

	"github.com/keshon/tars/internal/agent"
	"github.com/keshon/tars/internal/llm"
	"github.com/keshon/tars/internal/permission"
	"github.com/keshon/tars/internal/prompts"
	"github.com/keshon/tars/internal/tools"
	"github.com/keshon/tars/internal/workspace"
)

const (
	// inspectorSteps bounds a look-and-report agent. It cannot change
	// anything, so the only thing an unbounded one can do is read the
	// context window full.
	inspectorSteps = 8

	// subagentSteps bounds one delegated unit of work. Lower than a
	// top-level agent's budget by design: a subagent that needs more than
	// this was handed a task too big to delegate.
	subagentSteps = 12
)

// Env is what every role needs regardless of what it does. Build one per
// run and pass it to each constructor.
type Env struct {
	Client       llm.Client
	WS           *workspace.Workspace
	Procs        *tools.BackgroundProcesses
	MaxTokens    int
	ContextLimit int
	Policy       permission.Policy
	BackendKind  string

	// Model names the backend model for display (status, logs). Local
	// servers ignore it on the wire (they serve one model); it still
	// answers "what am I talking to". Empty renders as a dash.
	Model  string
	Stream bool

	// ReasoningBudget caps <think> deliberation per response in
	// characters. Zero means the agent default; negative disables
	// wrap-ups. Passed straight into agent.Config, which documents it.
	ReasoningBudget int

	// MCPTools are discovered MCP tools appended to the full tool set
	// (Interactive, Subagent, Worker — never the read-only Inspector).
	MCPTools []agent.Tool

	// OnDelta receives streamed content chunks for live display.
	OnDelta func(chunk string)

	// Gate, if set, prompts the operator for Ask-gated calls. Nil means
	// Ask degrades to Deny.
	Gate func(tool, resource string, args json.RawMessage) (permission.Effect, error)

	// OnStep, if set, receives every step of every agent built from this
	// Env, tagged with that agent's label ("explore", "review", a subtask
	// id). Callers used to wrap this by hand at four separate sites and
	// each wrapped it slightly differently.
	OnStep func(label string, step int, msg llm.Message)

	// OnToolResult, if set, receives every completed tool call result.
	// See agent.Config.OnToolResult. Nil keeps the historical silence.
	OnToolResult func(callID, result string)

	// OnUsage, if set, receives backend token counts per step.
	// See agent.Config.OnUsage.
	OnUsage func(step int, usage llm.Usage)

	// StateDir, when set, is where transcripts go; a role's stateFile
	// argument is resolved against it. Empty means the caller passes
	// whole paths, or none at all.
	StateDir string
}

func (e Env) onStep(label string) func(int, llm.Message) {
	if e.OnStep == nil {
		return nil
	}
	return func(step int, msg llm.Message) { e.OnStep(label, step, msg) }
}

func (e Env) gate() func(context.Context, string, string, json.RawMessage) (permission.Effect, error) {
	if e.Gate == nil {
		return nil
	}
	return func(_ context.Context, tool, resource string, args json.RawMessage) (permission.Effect, error) {
		return e.Gate(tool, resource, args)
	}
}

func (e Env) policy() permission.Policy {
	if len(e.Policy.Rules) == 0 {
		return permission.Default()
	}
	return e.Policy
}

func interactiveSystem(backendKind string) string {
	if backendKind == "" {
		return prompts.System
	}
	return prompts.SystemForBackend(backendKind)
}

// Planner is the read-only agent behind -plan: it investigates and
// proposes without the ability to change anything. Capability is removed by
// the tool set (ReadOnly), never by asking the model to hold back.
func Planner(e Env, label, stateFile string) *agent.Agent {
	return agent.New(agent.Config{
		Client:          e.Client,
		Tools:           tools.ReadOnly(e.WS, e.Procs),
		System:          prompts.PlanMode,
		MaxTokens:       e.MaxTokens,
		ContextLimit:    e.ContextLimit,
		ReasoningBudget: e.ReasoningBudget,
		StateFile:       e.statePath(stateFile),
		SkipVerify:      true,
		OnStep:          e.onStep(label),
		OnToolResult:    e.OnToolResult,
		OnUsage:         e.OnUsage,
		Policy:          e.policy(),
		BeforeToolCall:  e.gate(),
	})
}

func (e Env) statePath(name string) string {
	switch {
	case name == "":
		return ""
	case e.StateDir == "":
		return name
	default:
		return filepath.Join(e.StateDir, name)
	}
}

// Inspector looks and reports: read-only tools, a hard step bound, and no
// self-verify round. Used for codebase mapping and the end-of-mission
// review. It gets ReadOnly rather than Base because taking the mutating
// tools away beats asking a weak model not to reach for them.
func Inspector(e Env, label, system, stateFile string) *agent.Agent {
	return agent.New(agent.Config{
		Client:          e.Client,
		Tools:           tools.ReadOnly(e.WS, e.Procs),
		System:          system,
		MaxSteps:        inspectorSteps,
		MaxTokens:       e.MaxTokens,
		ContextLimit:    e.ContextLimit,
		ReasoningBudget: e.ReasoningBudget,
		SkipVerify:      true,
		StateFile:       e.statePath(stateFile),
		OnStep:          e.onStep(label),
		OnToolResult:    e.OnToolResult,
		OnUsage:         e.OnUsage,
		Policy:          e.policy(),
		BeforeToolCall:  e.gate(),
	})
}

// Subagent is what delegate_task spawns. It gets the full tool set minus
// ask_user and delegate_task: no subagent can spawn subagents, and none
// can block the process waiting on a human who is watching the parent.
func Subagent(e Env, role string) *agent.Agent {
	return agent.New(agent.Config{
		Client:          e.Client,
		Tools:           tools.Base(e.WS, e.Procs, e.MCPTools...),
		System:          prompts.WithRole(role),
		MaxSteps:        subagentSteps,
		MaxTokens:       e.MaxTokens,
		ContextLimit:    e.ContextLimit,
		ReasoningBudget: e.ReasoningBudget,
		SkipVerify:      true,
		Policy:          e.policy(),
		BeforeToolCall:  e.gate(),
		OnToolResult:    e.OnToolResult,
		OnUsage:         e.OnUsage,
	})
}

// Worker executes one mission subtask in a fresh context.
//
// expectsWrites says whether finishing with nothing written is a failure
// rather than an answer. Mission checks are mechanical, so the general
// verify round is redundant — except for a worker about to end having
// written nothing on a subtask that was supposed to write, which is the
// announce-without-write shape. See agent.Config.VerifyOnZeroWrites.
func Worker(e Env, label, system, stateFile string, maxSteps int, expectsWrites bool) *agent.Agent {
	return agent.New(agent.Config{
		Client:             e.Client,
		Tools:              tools.Base(e.WS, e.Procs, e.MCPTools...),
		System:             system,
		MaxSteps:           maxSteps,
		MaxTokens:          e.MaxTokens,
		ContextLimit:       e.ContextLimit,
		ReasoningBudget:    e.ReasoningBudget,
		SkipVerify:         true,
		VerifyOnZeroWrites: expectsWrites,
		StateFile:          e.statePath(stateFile),
		OnStep:             e.onStep(label),
		OnToolResult:       e.OnToolResult,
		OnUsage:            e.OnUsage,
		Policy:             e.policy(),
		BeforeToolCall:     e.gate(),
	})
}

// Interactive is the top-level agent a person talks to: the full tool set
// plus ask_user and delegate_task.
//
// ask performs the blocking question. Passing nil removes ask_user from
// the tool set entirely, which changes what the agent can do — an
// unattended caller should usually pass a responder that says plainly no
// human is available rather than dropping the tool, so that the agent
// under test has the same tools as the one that ships and reaching for
// ask_user stays visible in the trace.
//
// verify, if set, runs a real build/test command at the self-check
// checkpoint so the finish is gated on ground truth rather than a claim.
func Interactive(e Env, label, stateFile string,
	ask func(question string) (string, error),
	verify func(ctx context.Context) (string, bool)) *agent.Agent {

	extra := []agent.Tool{&tools.Delegate{Spawn: func(role string) *agent.Agent {
		return Subagent(e, role)
	}}}
	extra = append(extra, e.MCPTools...)
	if ask != nil {
		extra = append(extra, tools.AskUser{AskFn: ask})
	}

	return agent.New(agent.Config{
		Client:          e.Client,
		Tools:           tools.Base(e.WS, e.Procs, extra...),
		System:          interactiveSystem(e.BackendKind),
		MaxTokens:       e.MaxTokens,
		ContextLimit:    e.ContextLimit,
		ReasoningBudget: e.ReasoningBudget,
		StateFile:       e.statePath(stateFile),
		Verify:          verify,
		OnStep:          e.onStep(label),
		OnToolResult:    e.OnToolResult,
		OnUsage:         e.OnUsage,
		Policy:          e.policy(),
		BeforeToolCall:  e.gate(),
		Stream:          e.Stream,
		OnDelta:         e.OnDelta,
	})
}
