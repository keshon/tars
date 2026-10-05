package agent

import (
	"context"
	"encoding/json"

	"github.com/keshon/tars/internal/llm"
	"github.com/keshon/tars/internal/permission"
)

// Config wires together everything an Agent needs. There is exactly one
// loop implementation here — no separate "policy" or "runtime" layer
// sitting next to it making the same decisions twice.
type Config struct {
	Client llm.Client
	Tools  *Registry
	System string

	// MaxSteps bounds how many model round-trips a single Run performs.
	MaxSteps int

	// TodoFunding enables todo-driven step funding: acknowledged
	// checklist work (listed open items, completed items) extends the
	// step budget up to one extra base budget. Default off. Direct
	// interactive runs set it; bounded workers (mission subtasks,
	// subagents) run fixed budgets by construction.
	TodoFunding bool

	// MaxTokens caps generation length per response. Left at zero, New
	// defaults this to 8192 — large single-shot generations (a full
	// HTML+CSS+JS file in one write_file call) silently truncate mid-JSON
	// without enough budget, which looks like a model failure but is
	// actually a missing request parameter.
	MaxTokens int

	// ContextLimit is the backend's real context window size, e.g. from
	// KoboldClient.MaxContextLength. Zero disables budget tracking
	// entirely — there's no model-agnostic default that means anything,
	// so this has to come from the backend, not a guess.
	ContextLimit int

	// MaxStuckSteps is how many consecutive unproductive steps are
	// tolerated before the agent gets nudged to try a different approach.
	// A step counts as unproductive if every tool call in it errored, OR
	// if it's an exact repeat of the previous step's calls — a tool that
	// "succeeds" by returning the same listing for the third time in a
	// row is just as stuck as one that keeps erroring.
	MaxStuckSteps int

	// MaxExploratorySteps is how many consecutive steps with zero
	// Exclusive (mutating) tool calls are tolerated before a one-time
	// nudge suggests broadening the search instead of indefinitely
	// narrowing the same dead-end query. Distinct from MaxStuckSteps:
	// the model can be making "progress" by this loop's definition (new
	// list_files/grep_files calls, no errors, no exact repeats) while
	// still going nowhere — burning steps refining a search instead of
	// converging on an answer. Zero means "use the default of 8"; to
	// disable this nudge entirely, set it higher than MaxSteps.
	MaxExploratorySteps int

	// SkipVerify disables the self-check pass that normally runs once
	// before Run returns: the model is asked to re-examine its own work
	// against the original task before declaring victory. Leave this
	// false unless the extra round-trip genuinely isn't worth it for your
	// use case — a local model "succeeding" by writing an empty file is
	// exactly the kind of mistake this catches some of the time.
	SkipVerify bool

	// VerifyOnZeroWrites declares that this run is supposed to write
	// files, which turns a finish with zero successful MutatingTools calls
	// into a failure rather than an answer: the self-check round still
	// runs despite SkipVerify, and after that the finish is refused up to
	// MaxZeroWriteRefusals times. Mission workers set this per subtask —
	// their correctness is checked mechanically afterwards, so the general
	// verify round is redundant, but a worker that announces "let me write
	// the file" and stops is best corrected HERE, while its analysis is
	// still in context, rather than by a fix worker that has to rediscover
	// everything. Leave false for runs where writing nothing is a valid
	// outcome, or the loop will argue with a model that is right.
	VerifyOnZeroWrites bool

	// OnStep, if set, is called after every model response — for
	// logging/debugging without baking observability into the loop.
	OnStep func(step int, msg llm.Message)

	// OnUsage, if set, is called with the backend-reported token counts
	// for every model response. Separate from OnStep so existing callers
	// keep working; observers that meter context (TUIs, JSON streams)
	// wire this.
	OnUsage func(step int, usage llm.Usage)

	// OnToolResult, if set, is called for every completed tool call with
	// its result text (or "error: ..." on failure). Calls in one step run
	// concurrently when their mode allows, so this may fire from several
	// goroutines at once — implementations must synchronize. Nil means
	// silent, which is also the historical console behavior — tool results
	// never printed there. Observers that render transcripts (JSON event
	// streams, TUIs) wire this; the loop itself never prints.
	OnToolResult func(callID, result string)

	// StateFile, if set, gets the full message history written to it
	// (as JSON) after every step. If the process dies or the run hits
	// MaxSteps, the file on disk reflects the last completed step —
	// load it with LoadState and continue with Resume instead of losing
	// everything and starting over.
	StateFile string

	// MutatingTools names the tools that actually change the filesystem.
	// Used only so the self-check round can state a hard fact ("0 of
	// these succeeded this run") instead of trusting the model's own
	// claim that it saved something — a model can describe writing a
	// file in its response text without ever calling the tool that
	// actually does it. Defaults to this project's own file tools.
	MutatingTools []string

	// Verify, if set, runs once at the self-check checkpoint (a real
	// build/lint/test command, e.g. "go build ./... && go test ./...")
	// and its real output gets folded into the verify message as ground
	// truth — the same "don't trust the self-report" pattern as
	// MutatingTools, extended from "did you save the file" to "does the
	// code actually work." Nil means skip this (most projects don't have
	// one canned command that makes sense).
	Verify func(ctx context.Context) (output string, ok bool)

	// CompactKeepSteps is how many recent assistant-led step groups to
	// retain when history is mechanically compacted. Defaults to 8 in New.
	// Set to -1 to disable compaction.
	CompactKeepSteps int

	// CompactAtPercent is the share of ContextLimit at which history is
	// compacted. Defaults to defaultCompactAtPercent.
	//
	// The window is not the model's working range. A local model holds a
	// plan across far less context than it will accept, so compacting at
	// the point where the window is nearly full means every step between
	// "degraded" and "full" already ran degraded. Live: a run reached
	// 57k of a 65k window, collapsed into a single repeated token, and
	// then described the truncation notice as though it were the task.
	//
	// The right value is a property of the model, not of this code, which
	// is why it is configuration with a documented default rather than a
	// constant chosen once and hidden.
	CompactAtPercent int

	// ReasoningBudget caps <think> deliberation per response in
	// characters (chars/4 approximates tokens — no backend on the wire
	// reports reasoning tokens separately, verified on koboldcpp).
	// A finish attempt carrying more thinking than this gets one
	// wrap-up round demanding commitment instead of more deliberation.
	// Zero means the default; negative disables wrapping entirely.
	ReasoningBudget int

	// MaxThinkWraps bounds wrap-up rounds per run. A model that ignores
	// the wrap-up and rambles again degrades to the status quo after
	// this many: the step budget, not another nudge, ends the run.
	// Zero means the default.
	MaxThinkWraps int

	// Policy gates tool calls before they run. Nil means allow-all.
	Policy permission.Policy
	// BeforeToolCall, if set, decides whether a call runs. It returns the
	// policy effect plus the resource string used for matching (command,
	// path, URL). Ask means prompt the operator; the hook itself performs
	// the prompt and returns an error to block. Nil means no prompting:
	// Ask degrades to Deny.
	BeforeToolCall func(ctx context.Context, tool, resource string, args json.RawMessage) (permission.Effect, error)

	// AfterToolCall, if set, observes every completed call.
	AfterToolCall func(tool, resource, result string, err error)

	// Stream, when true, uses SSE streaming (llm.Server.Stream) with
	// OnDelta per content chunk instead of one blocking Chat. Only
	// effective on backends with SSE support (openai/llama); koboldcpp
	// refuses and the loop falls back to ChatWithRetry.
	Stream bool

	// OnDelta receives streamed content chunks for live display.
	OnDelta func(chunk string)

	// OnNudge receives loop-generated harness text (verify rounds,
	// refusals, leak notices, wrap-ups, stuck escalations) with a
	// kind tag. Nil disables reporting; the [harness] provenance
	// prefix applies regardless, so the model always sees what the
	// operator would see.
	OnNudge func(kind, text string)
}

// DefaultMaxSteps bounds a run that names no budget. Shared with the
// TUI's step meter so the displayed limit is the enforced one, never a
// second literal drifting beside it.
const DefaultMaxSteps = 25

// DefaultCompactKeepSteps is how many recent step groups compaction
// keeps, manual or automatic. One literal, not two eights.
const DefaultCompactKeepSteps = 8

func New(cfg Config) *Agent {
	if cfg.MaxSteps == 0 {
		cfg.MaxSteps = DefaultMaxSteps
	}
	if cfg.MaxStuckSteps == 0 {
		cfg.MaxStuckSteps = 2
	}
	if cfg.MaxExploratorySteps == 0 {
		cfg.MaxExploratorySteps = 8
	}
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = 8192
	}
	if len(cfg.MutatingTools) == 0 {
		cfg.MutatingTools = []string{"write_file", "patch_file", "patch_lines", "move_file"}
	}
	if cfg.CompactKeepSteps == 0 {
		cfg.CompactKeepSteps = DefaultCompactKeepSteps
	}
	if cfg.CompactAtPercent == 0 {
		cfg.CompactAtPercent = defaultCompactAtPercent
	}
	if cfg.ReasoningBudget == 0 {
		cfg.ReasoningBudget = defaultReasoningBudget
	}
	if cfg.MaxThinkWraps == 0 {
		cfg.MaxThinkWraps = defaultMaxThinkWraps
	}
	return &Agent{cfg: cfg}
}

// defaultReasoningBudget caps deliberation per response. Roughly 1500
// tokens at chars/4 against the 8192 default generation budget.
// Calibrated live: Qwen 3.8 deliberated 119-502 chars on trivial tasks,
// so 6000 gives ~12x headroom - a genuine ramble trips it clearly while
// normal deliberation never comes close. Negative disables wrapping.
const defaultReasoningBudget = 6000

// defaultMaxThinkWraps bounds wrap-up rounds per run.
const defaultMaxThinkWraps = 2
