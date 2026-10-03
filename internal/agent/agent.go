package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/keshon/tars/internal/llm"
	"github.com/keshon/tars/internal/permission"
	"github.com/keshon/tars/internal/prompts"
	"github.com/keshon/tars/internal/session"
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

type Agent struct {
	cfg Config

	// LastRunMutations counts successful MutatingTools calls from the
	// most recent completed run. Set when Run/Resume returns; readable by
	// delegate_task to report subagent filesystem changes to the parent.
	// (Unlike RunReport.MutatedPaths, this also counts writes made by
	// delegated subagents, via the DELEGATE result header.)
	LastRunMutations int

	report RunReport
}

// MutatedPaths returns the workspace paths mutated by the last run,
// for session-end sweeps. Empty when nothing mutated.
func (a *Agent) MutatedPaths() []string {
	return a.report.MutatedPaths
}

// RunReport is what a harness can learn about a finished run without
// trusting anything the model said about itself: which files its own
// mutating tool calls actually touched, how many steps it took, what the
// final message was. Populated by run() as measured fact — a mission
// runner records these into its ledger instead of asking a weak model to
// summarize its own work, which is exactly where fake "I saved the file"
// claims come from.
type RunReport struct {
	// Steps is how many model round-trips the run performed.
	Steps int

	// MutatedPaths lists the workspace paths of successful MutatingTools
	// calls made directly by this agent (path/from/to arguments), in
	// first-touch order, deduplicated. Subagent writes are not included —
	// a parent that needs those reads the DELEGATE result header.
	MutatedPaths []string

	// Final is the model's final answer text ("" if the run errored out).
	Final string

	// LastPromptTokens is the backend-reported prompt size of the last
	// completed call — how full the context actually got.
	LastPromptTokens int
}

// ToolNames returns the names of the tools this agent can call, sorted.
// What an agent is allowed to do is a property worth asserting on rather
// than assuming: a harness that builds its agent by hand can silently end
// up with a different tool set than the one that ships.
func (a *Agent) ToolNames() []string {
	if a.cfg.Tools == nil {
		return nil
	}
	return a.cfg.Tools.Names()
}

// Report returns measured facts about the most recent Run/Resume,
// including a partially filled report for a run that errored mid-way.
func (a *Agent) Report() RunReport {
	return a.report
}

func New(cfg Config) *Agent {
	if cfg.MaxSteps == 0 {
		cfg.MaxSteps = 25
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
		cfg.CompactKeepSteps = 8
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

// Run executes the agent loop on a single task and returns the model's
// final answer once it stops requesting tools.
func (a *Agent) Run(ctx context.Context, task string) (string, error) {
	return a.run(ctx, []llm.Message{
		{Role: llm.RoleSystem, Content: a.cfg.System},
		{Role: llm.RoleUser, Content: task},
	})
}

// Resume continues from a previously saved history (see LoadState) after
// a run was interrupted — crashed, killed, or hit MaxSteps. note is
// appended as a fresh user message before continuing; pass something like
// "Your previous attempt was interrupted before finishing. Check the
// current state of the workspace before assuming anything, then finish
// the task." A plain summary-of-what-happened isn't required — the full
// history is already there, the model can re-read it.
func (a *Agent) Resume(ctx context.Context, history []llm.Message, note string) (string, error) {
	history = append(history, llm.Message{Role: llm.RoleUser, Content: note})
	return a.run(ctx, history)
}

// PausedOnQuestion reports whether the last message in history is an
// assistant message containing an unanswered ask_user tool call. This is
// the correct way to distinguish "interrupted by a question" from
// "interrupted by a crash/timeout" when deciding which resume path to
// take.
func PausedOnQuestion(history []llm.Message) (callID string, question string, ok bool) {
	if len(history) == 0 {
		return "", "", false
	}
	last := history[len(history)-1]
	if last.Role != llm.RoleAssistant {
		return "", "", false
	}
	for _, tc := range last.ToolCalls {
		if tc.Name == "ask_user" {
			var args struct {
				Question string `json:"question"`
			}
			json.Unmarshal(tc.Arguments, &args)
			return tc.ID, args.Question, true
		}
	}
	return "", "", false
}

// ResumeWithAnswer continues from a history that ended with an unanswered
// ask_user tool call. answer is synthesized as the proper role:tool,
// tool_call_id message before the loop resumes — NOT as a plain user
// message, which would produce a wire-invalid conversation (a backend
// expects every tool_call to get its matching tool-result before anything
// else, not a naked user message after an unanswered call).
func (a *Agent) ResumeWithAnswer(ctx context.Context, history []llm.Message, callID, answer string) (string, error) {
	history = append(history, llm.Message{
		Role:       llm.RoleTool,
		ToolCallID: callID,
		Content:    answer,
	})
	return a.run(ctx, history)
}

// LoadState reads a history previously written via Config.StateFile.
func LoadState(path string) ([]llm.Message, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read state file: %w", err)
	}
	var history []llm.Message
	if err := json.Unmarshal(data, &history); err != nil {
		return nil, fmt.Errorf("decode state file: %w", err)
	}
	return history, nil
}

// saveState is best-effort: a failed snapshot write should never abort a
// run that's otherwise working fine. Writes are atomic (tmp+rename) so a
// crash mid-write never leaves corrupt JSON behind.
func (a *Agent) saveState(history []llm.Message) {
	if a.cfg.StateFile == "" {
		return
	}
	data, err := json.MarshalIndent(history, "", "  ")
	if err != nil {
		return
	}
	if dir := filepath.Dir(a.cfg.StateFile); dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	tmp := a.cfg.StateFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, a.cfg.StateFile)
	session.Append(a.cfg.StateFile, len(history), history)
}

// toolResource extracts the policy-matched resource from a call's args:
// command for shell/background, path for file tools, url for check_url.
func toolResource(name string, args json.RawMessage) string {
	var fields struct {
		Command string `json:"command"`
		Path    string `json:"path"`
		From    string `json:"from"`
		To      string `json:"to"`
		URL     string `json:"url"`
		ID      string `json:"id"`
	}
	if json.Unmarshal(args, &fields) != nil {
		return ""
	}
	switch name {
	case "run_shell", "start_background":
		return fields.Command
	case "read_file", "write_file", "patch_file", "patch_lines":
		return fields.Path
	case "move_file":
		return fields.From + "->" + fields.To
	case "check_url":
		return fields.URL
	default:
		if fields.Path != "" {
			return fields.Path
		}
		if fields.Command != "" {
			return fields.Command
		}
		return string(args)
	}
}

type runState struct {
	stuckSteps, exploratorySteps, mutatingSucceeded, warnedThreshold, lastPromptTokens int
	consecutiveSameToolCount                                                           int
	verifiedOnce, searchFatigueWarned, blockFinishDueToVerify, toolLoopWarned          bool
	emptyFinishRetried                                                                 bool
	lastSignature, verifyFailedOutput, lastSingleTool                                  string
	mutatedPaths                                                                       []string

	// zeroWriteFinishes counts how many times the model has tried to end
	// the run having written nothing, while VerifyOnZeroWrites says the
	// run was supposed to write. See MaxZeroWriteRefusals.
	zeroWriteFinishes int

	// toolCalls counts tool calls issued this run. Gates the first
	// verify round together with claimsFileEffects below: a finish
	// with no tool work and no file-effect claims behind it is
	// chit-chat, not work to verify. The one-shot stays armed, so a
	// later working turn still gets verified.
	toolCalls int

	// thinkWraps counts wrap-up rounds this run: finishes attempted with
	// more reasoning than ReasoningBudget allows. Bounded by
	// MaxThinkWraps; past it the run degrades to finishing as-is.
	thinkWraps int

	// pendingBudget holds a context-usage notice that lost its slot to a
	// more urgent nudge, to be delivered on the next step that has one
	// free. See interject.
	pendingBudget string

	// failedCalls counts consecutive identical failures per call, cleared
	// on success or on any workspace mutation. See maxIdenticalAttempts.
	failedCalls map[string]int

	// stuckNudges counts how many times this run has been told it is
	// stuck, so the wording can escalate instead of repeating.
	stuckNudges int

	// idempotentSeen maps signature (name+args) of successful idempotent
	// calls to the step that ran them; cleared whenever anything mutates
	// the workspace. Exact repeats short-circuit to an error result — a
	// weak model that re-issues the same read five times gets told it's
	// repeating instead of getting five copies of the same bytes.
	idempotentSeen map[string]int

	// wrotePaths maps write_file path → step of the successful write.
	// Live failure (2026-07-14): weak model rewrote package.json with
	// identical content in a tight loop; soft ToolLoop/StuckRepeating
	// nudges were ignored, and each "ok" reset stuckSteps because
	// progressed==true. A second write_file to the same path is refused
	// so the model must write the next file or finish.
	wrotePaths map[string]int
}

func (a *Agent) run(ctx context.Context, history []llm.Message) (string, error) {
	var st runState
	a.LastRunMutations = 0
	a.report = RunReport{}

	for step := 0; step < a.cfg.MaxSteps; step++ {
		resp, err := a.chat(ctx, llm.ChatRequest{
			Messages:  history,
			Tools:     a.cfg.Tools.Defs(),
			MaxTokens: a.effectiveMaxTokens(st.lastPromptTokens),
		})
		if err != nil {
			// A full context is recoverable: compact aggressively and let
			// the next step continue with the summary. Anything else is
			// infrastructure - mission treats it as resumable errInfra.
			//
			// The step-0 case matters: the first call can overflow (a huge
			// task plus system prompt on a small window), when history holds
			// nothing compactable. Then there is nothing to drop, but the
			// run must still continue with the nudge rather than die — probe
			// 18 failed exactly this way before the guard was split.
			if llm.IsOverflow(err) {
				if len(history) > 2 {
					keep := a.cfg.CompactKeepSteps
					if keep <= 0 {
						keep = 8
					}
					history = compactHistory(history, keep/2)
					a.saveState(history)
				}
				history = a.nudge(history, NudgeOverflow, prompts.OverflowRecovered)
				continue
			}
			return "", fmt.Errorf("step %d: chat: %w", step, err)
		}
		// A model can write a tool call as text instead of making a
		// structured one. The parser recovers well-formed ones (known
		// tool, parseable args) into real calls, which then run through
		// the normal tool path below - guards, permissions, repeat
		// detection and all. Unparseable remainders keep the nudge path
		// in the no-tool-calls branch; `leakedText` carries that verdict
		// down so the branch does not re-detect.
		var leakedText bool
		if len(resp.Message.ToolCalls) == 0 && resp.FinishReason != "length" {
			known := map[string]bool{}
			if a.cfg.Tools != nil {
				for _, n := range a.cfg.Tools.Names() {
					known[n] = true
				}
			}
			var recovered []llm.LeakedCall
			var cleaned string
			recovered, cleaned, leakedText = llm.ExtractLeakedCalls(resp.Message.Content, known)
			for i, rc := range recovered {
				resp.Message.ToolCalls = append(resp.Message.ToolCalls, llm.ToolCall{
					ID:        llm.LeakCallID(step, i),
					Name:      rc.Name,
					Arguments: rc.Arguments,
				})
			}
			resp.Message.Content = cleaned
		}
		if a.cfg.OnStep != nil {
			a.cfg.OnStep(step, resp.Message)
		}
		// Usage rides alongside OnStep rather than inside it: changing
		// OnStep's signature would break every caller (roles, mission,
		// eval, CLI) for data only some observers want.
		if a.cfg.OnUsage != nil {
			a.cfg.OnUsage(step, resp.Usage)
		}
		history = append(history, resp.Message)
		st.toolCalls += len(resp.Message.ToolCalls)
		budgetNudge := a.budgetWarning(resp.Usage, &st.warnedThreshold)
		if resp.Usage.PromptTokens > 0 {
			st.lastPromptTokens = resp.Usage.PromptTokens
		}
		a.report.Steps = step + 1
		a.report.LastPromptTokens = st.lastPromptTokens
		a.maybeCompact(&history, resp.Usage, &st)
		a.saveState(history)

		if len(resp.Message.ToolCalls) == 0 {
			// finish_reason "length" means the backend cut generation off
			// and discarded whatever the model was building — usually the
			// tool call it had just announced. That's a truncation, never a
			// finish: treating the stump as an answer is how a live run
			// "finished" a subtask with "Let me write the plan.md file…"
			// and zero writes. Nudge and continue; escalate via the stuck
			// counter so a backend that truncates every response is still
			// bounded by MaxStuckSteps → MaxSteps.
			if resp.FinishReason == "length" {
				history = a.nudge(history, NudgeTruncated, prompts.Truncated)
				st.stuckSteps++
				if st.stuckSteps >= a.cfg.MaxStuckSteps {
					history = a.nudge(history, NudgeStuck, prompts.StuckFailing)
					st.stuckSteps = 0
				}
				continue
			}

			// A finish attempt carrying more reasoning than the budget is a
			// model about to end the run on deliberation rather than a
			// decision: wrap it up and demand commitment instead. Acting
			// (tool calls above) is progress by definition, so only
			// finishes are wrapped; a wrap-up round that yields another
			// ramble counts as unproductive like any other, and past
			// MaxThinkWraps the run degrades to finishing as-is.
			if a.cfg.ReasoningBudget > 0 && st.thinkWraps < a.cfg.MaxThinkWraps {
				if thought := llm.DeliberationChars(resp.Message); thought > a.cfg.ReasoningBudget {
					st.thinkWraps++
					st.stuckSteps++
					history = a.nudge(history, NudgeThinkWrap, fmt.Sprintf(prompts.ThinkWrapUp, thought, a.cfg.ReasoningBudget))
					a.saveState(history)
					continue
				}
			}

			// A model can write text that *looks* like a tool call
			// ("<|tool_call>call:write_file{...}") instead of making a
			// real structured one. Recoverable calls were extracted above;
			// this branch sees only the unparseable remainder. Nothing here
			// executes, but the model often
			// then believes — and later claims — that it did. Catch this
			// before it's mistaken for a genuine finish.
			if leakedText {
				history = a.nudge(history, NudgeLeak, prompts.LeakDetected)
				st.stuckSteps++
				if st.stuckSteps >= a.cfg.MaxStuckSteps {
					history = a.nudge(history, NudgeLeak, prompts.LeakRepeated)
					st.stuckSteps = 0
				}
				continue
			}

			if st.blockFinishDueToVerify {
				st.blockFinishDueToVerify = false
				st.verifiedOnce = false
				history = a.nudge(history, NudgeVerify, fmt.Sprintf(prompts.VerifyFailedContinue, st.verifyFailedOutput))
				st.verifyFailedOutput = ""
				continue
			}

			verifyWanted := !a.cfg.SkipVerify ||
				(a.cfg.VerifyOnZeroWrites && st.mutatingSucceeded == 0)
			// Tool-dependent: verify when the run did tool work, when
			// writes were expected but none landed, or when the answer
			// claims file effects. Pure chit-chat skips (falling through
			// to finish evaluation below); the one-shot stays armed for
			// a later working turn. The workspace verdict below remains
			// the authority either way — this round is a recovery chance,
			// not the score.
			needsVerify := st.toolCalls > 0 ||
				(a.cfg.VerifyOnZeroWrites && st.mutatingSucceeded == 0) ||
				claimsFileEffects(resp.Message.Content)
			if verifyWanted && !st.verifiedOnce && needsVerify {
				st.verifiedOnce = true
				verifyMsg := prompts.Verify
				if st.mutatingSucceeded == 0 {
					verifyMsg += fmt.Sprintf(prompts.VerifyZeroWrites,
						strings.Join(a.cfg.MutatingTools, "/"))
				}
				if a.cfg.Verify != nil {
					if out, ok := a.cfg.Verify(ctx); ok {
						if out == "" {
							out = "(no output)"
						}
						verifyMsg += fmt.Sprintf(prompts.VerifyCheckResult, out)
						if strings.HasPrefix(out, "FAILED") {
							st.blockFinishDueToVerify = true
							st.verifyFailedOutput = out
						}
					}
				}
				history = a.nudge(history, NudgeVerify, verifyMsg)
				if budgetNudge != "" {
					history = a.nudge(history, NudgeBudget, budgetNudge)
				}
				continue
			}
			// Announcing the work is not doing the work. A worker that ends
			// its turn with "Now let me write the plan.md file" and no tool
			// call has produced nothing, and its own narration is the only
			// evidence to the contrary. The verify round above catches the
			// FIRST such finish — but only the first, because verifiedOnce
			// is a one-shot. Live failure (2026-07-14, parseurl mission):
			// the worker announced, took the verify nudge, announced again,
			// and the second announcement was accepted as a finish. All
			// three subtask attempts died in exactly that shape.
			//
			// While writes are still expected and none have landed, refuse
			// the finish and quote the model's own claim back at it. Bounded
			// by MaxZeroWriteRefusals: a few cheap in-context retries beat a
			// fresh fix worker that has to rediscover the whole subtask.
			if a.cfg.VerifyOnZeroWrites && st.mutatingSucceeded == 0 {
				if st.zeroWriteFinishes < MaxZeroWriteRefusals {
					st.zeroWriteFinishes++
					history = a.nudge(history, NudgeRefusal, fmt.Sprintf(prompts.AnnouncedNotWritten,
						lastClaim(resp.Message.Content),
						strings.Join(a.cfg.MutatingTools, "/")))
					a.saveState(history)
					continue
				}
				// Refusals exhausted and still nothing written: end the
				// run as a failure, not a narration-shaped success.
				// Wrapping ErrMaxSteps keeps mission retry semantics (a
				// stuck worker can converge; a dead backend cannot).
				return "", fmt.Errorf("%w: announced completion %d times without writing anything",
					ErrMaxSteps, st.zeroWriteFinishes)
			}
			if strings.TrimSpace(resp.Message.Content) == "" {
				if !st.emptyFinishRetried {
					st.emptyFinishRetried = true
					continue
				}
				return "", fmt.Errorf("%w: empty finish after retry", ErrMaxSteps)
			}
			a.LastRunMutations = st.mutatingSucceeded
			a.report.MutatedPaths = st.mutatedPaths
			a.report.Final = resp.Message.Content
			return resp.Message.Content, nil
		}

		signature := callSignature(resp.Message.ToolCalls)
		repeat := signature == st.lastSignature
		st.lastSignature = signature

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
		type callResult struct {
			content string
			err     error
		}
		results := make([]callResult, len(resp.Message.ToolCalls))

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
		skipped := make([]bool, len(resp.Message.ToolCalls))
		for i, call := range resp.Message.ToolCalls {
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
		for i, call := range resp.Message.ToolCalls {
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
			call := resp.Message.ToolCalls[i]
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
		for i, call := range resp.Message.ToolCalls {
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
		// same read can now legitimately return something new.
		if exclusiveSucceeded || st.mutatingSucceeded > mutatingBefore {
			st.idempotentSeen = nil
			// The same reasoning: once the workspace changed, a call that
			// failed before may now succeed.
			st.failedCalls = nil
		}

		// A step that changed the workspace is progress by definition, so
		// it can never be evidence of a loop. Writing four different files
		// in a row is the normal shape of a scaffolding subtask — it is
		// write_file four times, which is exactly what the same-tool
		// counter used to flag. Live failure (2026-07-14, FPS mission):
		// package.json, vite.config.ts and index.html were written
		// correctly, tripped the nudge at three, and the worker was told
		// to "switch to a genuinely different tool" mid-scaffold.
		// src/main.ts — the third acceptance criterion — was never written.
		//
		// Exact repeats are already handled by callSignature/idempotentSeen
		// and wrotePaths, so what's left for the same-tool counter is the
		// narrow case that nudge was written for: grinding one read-only
		// tool with slightly different arguments and never converging.
		mutated := st.mutatingSucceeded > mutatingBefore
		if mutated {
			st.exploratorySteps = 0
			st.lastSingleTool = ""
			st.consecutiveSameToolCount = 0
		} else {
			st.exploratorySteps++
			a.trackSingleToolLoop(resp.Message.ToolCalls, &st)
		}
		if progressed && !repeat {
			st.stuckSteps = 0
		} else {
			st.stuckSteps++
		}

		if msg := a.interject(&st, stepOutcome{repeat: repeat, budget: budgetNudge}); msg != "" {
			history = append(history, llm.Message{Role: llm.RoleUser, Content: msg})
		}
		a.saveState(history)
	}

	a.LastRunMutations = st.mutatingSucceeded
	a.report.MutatedPaths = st.mutatedPaths
	return "", fmt.Errorf("%w (%d) without finishing", ErrMaxSteps, a.cfg.MaxSteps)
}

// chat runs one model turn: streaming when configured and supported,
// otherwise ChatWithRetry. Streaming gets a single attempt (a partial
// stream cannot resume mid-turn); the unary path retries.
func (a *Agent) chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	if a.cfg.Stream {
		if s, ok := a.cfg.Client.(interface {
			Stream(context.Context, llm.ChatRequest, func(string)) (llm.ChatResponse, error)
		}); ok {
			resp, err := s.Stream(ctx, req, a.cfg.OnDelta)
			if err == nil {
				return resp, nil
			}
			if !strings.Contains(strings.ToLower(err.Error()), "streaming unsupported") {
				return resp, err
			}
		}
	}
	return llm.ChatWithRetry(ctx, a.cfg.Client, req, llm.DefaultRetryPolicy())
}

// ErrMaxSteps marks a run that did real work but ran out of step budget —
// as opposed to infrastructure failures (a dead backend, a network error)
// where no work happened at all. Callers that retry on failure (the
// mission fix loop) must distinguish the two: retrying a step-budget
// death can converge; retrying against a dead backend just burns retry
// budget on connection errors.
var ErrMaxSteps = errors.New("reached max steps")

// mutatedPathsFromCall pulls workspace paths out of a mutating tool
// call's arguments. The project's file tools name them path (write_file,
// patch_file, patch_lines) or from/to (move_file); a mutating tool with
// none of these contributes nothing rather than guessing.
func mutatedPathsFromCall(args json.RawMessage) []string {
	var fields struct {
		Path string `json:"path"`
		From string `json:"from"`
		To   string `json:"to"`
	}
	if json.Unmarshal(args, &fields) != nil {
		return nil
	}
	var out []string
	for _, p := range []string{fields.Path, fields.From, fields.To} {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func writePathFromArgs(args json.RawMessage) string {
	var fields struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(args, &fields) != nil || fields.Path == "" {
		return ""
	}
	return strings.TrimPrefix(strings.ReplaceAll(fields.Path, "\\", "/"), "./")
}

// parseDelegateMutations reads the structured DELEGATE header from a
// delegate_task result so the parent run can count subagent writes.
func parseDelegateMutations(content string) (int, []string) {
	if !strings.HasPrefix(content, "DELEGATE\n") {
		return 0, nil
	}
	mutations := 0
	var paths []string
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "mutations: ") {
			var n int
			if _, err := fmt.Sscanf(line, "mutations: %d", &n); err == nil {
				mutations = n
			}
			continue
		}
		if p, ok := strings.CutPrefix(line, "paths: "); ok {
			if p = strings.TrimSpace(p); p != "" {
				paths = append(paths, p)
			}
			continue
		}
		if line == "----" {
			break
		}
	}
	return mutations, paths
}

// defaultCompactAtPercent is the share of the context window at which
// history is compacted. Well below the old 90%: a window is what the
// backend will accept, not what the model can still reason over, and the
// steps taken between those two points are the ones that produce
// confidently wrong work.
const defaultCompactAtPercent = 60

// maybeCompact drops old step groups once the prompt passes the
// threshold, and may do so more than once in a run — a long run that
// compacted at step 12 and then grew again is in exactly the state
// compaction exists for.
//
// compactHistory returns its input unchanged when there is nothing left
// to drop, which is what stops a full history from being compacted every
// step to no effect.
func (a *Agent) maybeCompact(history *[]llm.Message, usage llm.Usage, st *runState) bool {
	if a.cfg.ContextLimit <= 0 || a.cfg.CompactKeepSteps <= 0 || usage.PromptTokens <= 0 {
		return false
	}
	if usage.PromptTokens*100/a.cfg.ContextLimit < a.cfg.CompactAtPercent {
		return false
	}
	// Small windows compact on percent alone; large windows also require
	// enough absolute tokens to be worth the churn.
	if a.cfg.ContextLimit >= 32000 && usage.PromptTokens < pruneMinimumTokens {
		return false
	}
	compacted := compactHistory(*history, a.cfg.CompactKeepSteps)
	if len(compacted) >= len(*history) {
		return false
	}
	*history = compacted
	return true
}

// trackSingleToolLoop counts consecutive steps where the model issued
// exactly one tool call and it's the same tool name as the previous
// such step — catches run_shell/git-log tweak loops that exact-repeat
// detection misses because the arguments differ slightly each time.
// maxSameToolSteps is how many consecutive non-mutating steps calling one
// tool alone trip the tool-loop nudge.
const maxSameToolSteps = 3

// maxIdenticalAttempts is how many identical attempts at a call are
// allowed while it keeps failing and nothing mutates in between: the
// call executes twice and the third attempt is refused without running.
// Above two so a genuinely time-dependent retry has room — polling a
// server that is still starting is the honest case, and start_background
// is Concurrent so it does not clear this cache.
const maxIdenticalAttempts = 3

// stepOutcome carries the parts of a completed step that interject can't
// read off runState.
type stepOutcome struct {
	// repeat is true when this step's tool calls were byte-identical to
	// the previous step's — it picks StuckRepeating over StuckFailing.
	repeat bool

	// budget is a context-usage notice for this step, or "" for none.
	budget string
}

// interject picks at most ONE thing to say to the model at the end of a
// step that made tool calls.
//
// It replaces six independent append sites, all of which could fire in
// the same step: a worker could receive a search-fatigue nudge, a
// tool-loop warning, a budget notice and a stuck escalation at once, in
// source order rather than importance order. A 4B-active model given four
// corrections simultaneously follows none of them — and some of them
// contradict each other outright ("broaden your search" against "stop
// searching and act"). One signal, chosen by severity, is the whole point.
//
// The latches keep each advisory to once per run; without them a nudge
// repeats every step for as long as its counter stays over the line,
// which is its own kind of noise.
// Nudge kinds tag loop-generated harness text for display filtering
// and audit. Single source of truth (docs/rpc.md lists the same set);
// untyped so call sites need no conversions.
const (
	NudgeOverflow  = "overflow"
	NudgeTruncated = "truncated"
	NudgeThinkWrap = "think-wrap"
	NudgeLeak      = "leak"
	NudgeVerify    = "verify"
	NudgeRefusal   = "refusal"
	NudgeBudget    = "budget"
	NudgeStuck     = "stuck"
)

// harnessText marks loop-generated text as harness provenance. The
// model that mused "probably meta instructions from the user" was
// reading bare instructions; "[harness] ..." names the author inline,
// in history, events, and transcripts alike.
func harnessText(text string) string {
	return "[harness] " + text
}

// notifyNudge reports harness text to the observer, nil-safe.
func (a *Agent) notifyNudge(kind, text string) {
	if a.cfg.OnNudge != nil {
		a.cfg.OnNudge(kind, text)
	}
}

// nudge appends harness text to history and reports it. Every
// loop-generated message goes through here (or markNudge below) so
// observers see exactly what the model saw — never a paraphrase.
func (a *Agent) nudge(history []llm.Message, kind, text string) []llm.Message {
	marked := harnessText(text)
	a.notifyNudge(kind, marked)
	return append(history, llm.Message{Role: llm.RoleUser, Content: marked})
}

// markNudge reports harness text and returns it marked, for call sites
// (interject) that append history themselves.
func (a *Agent) markNudge(kind, text string) string {
	marked := harnessText(text)
	a.notifyNudge(kind, marked)
	return marked
}

func (a *Agent) interject(st *runState, o stepOutcome) string {
	// A budget notice fires once per threshold crossed, so it has to
	// queue rather than be dropped when something more urgent takes the
	// slot — otherwise the run never hears about it again.
	if o.budget != "" {
		st.pendingBudget = o.budget
	}

	switch {
	case st.stuckSteps >= a.cfg.MaxStuckSteps:
		// Hardest signal: nothing has worked for MaxStuckSteps running.
		//
		// This has no latch, unlike the nudges below, because being stuck
		// can recur for a new reason. What it must not do is say the same
		// thing twice: a live worker received the identical StuckFailing
		// text four times in one run and ignored all four. After the
		// first, the wording escalates; after the second, the loop stops
		// talking and lets the step budget end the run, because a third
		// copy of advice already refused twice is only context.
		st.stuckSteps = 0
		st.lastSignature = "" // the nudge itself breaks the repeat chain
		st.stuckNudges++
		switch {
		case st.stuckNudges == 1 && o.repeat:
			return a.markNudge(NudgeStuck, prompts.StuckRepeating)
		case st.stuckNudges == 1:
			return a.markNudge(NudgeStuck, prompts.StuckFailing)
		case st.stuckNudges == 2:
			return a.markNudge(NudgeStuck, prompts.StuckEscalated)
		default:
			return ""
		}

	case st.consecutiveSameToolCount >= maxSameToolSteps && !st.toolLoopWarned:
		st.toolLoopWarned = true
		return a.markNudge(NudgeStuck, prompts.ToolLoop)

	case st.exploratorySteps >= a.cfg.MaxExploratorySteps && !st.searchFatigueWarned:
		st.searchFatigueWarned = true
		return a.markNudge(NudgeStuck, prompts.SearchFatigue)
	}

	msg := st.pendingBudget
	st.pendingBudget = ""
	if msg != "" {
		return a.markNudge(NudgeBudget, msg)
	}
	return msg
}

// MaxZeroWriteRefusals bounds how many times a run that was supposed to
// write files may try to finish having written none. Each refusal costs
// one cheap in-context step; past this the subtask goes back to the
// mission layer, which has the failing check output and can seed a fix
// worker with real evidence instead of another nudge.
const MaxZeroWriteRefusals = 3

// lastClaim returns the final non-empty line of a model message, trimmed
// to something quotable. Quoting the model's own sentence back is the
// point: a generic "you wrote nothing" nudge reads as boilerplate to a
// weak model and gets answered with more narration, while its own words
// are the one piece of context it can't pattern-match past.
func lastClaim(content string) string {
	lines := strings.Split(strings.TrimSpace(content), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		const maxQuote = 200
		if r := []rune(line); len(r) > maxQuote {
			line = string(r[:maxQuote]) + "…"
		}
		return line
	}
	return "(nothing)"
}

// fileClaimVerbs are past-tense effect verbs: claims of completed file
// work. Read-verbs are deliberately absent (reads aren't effects);
// future tense is absent too (promises aren't claims).
var fileClaimVerbs = []string{
	"created", "wrote", "written", "saved", "updated", "added",
	"deleted", "removed", "modified", "patched", "moved", "renamed", "fixed",
}

// fileToken matches a filename, path, or definite file reference:
// dotted name, slash path, or "the/this/that file(s)". The last class
// catches the canonical fake-save phrasing ("I created the file")
// that names no path.
var fileToken = regexp.MustCompile(`[A-Za-z0-9_][A-Za-z0-9_.\-]*(?:\.[A-Za-z0-9]{1,5}|\/[A-Za-z0-9_.\-]+)|\b(?:the|this|that) files?\b`)

// claimsFileEffects reports whether answer text claims file effects: a
// past-tense effect verb plus a file token. Heuristic with a fail-safe
// direction: false positives cost today's verify round; false negatives
// (claim without a filename, e.g. "updated the config") skip like
// chit-chat and surface at the workspace verdict, which stays authoritative.
func claimsFileEffects(text string) bool {
	lower := strings.ToLower(text)
	hasVerb := false
	for _, v := range fileClaimVerbs {
		if strings.Contains(lower, v) {
			hasVerb = true
			break
		}
	}
	if !hasVerb {
		return false
	}
	return fileToken.MatchString(text)
}

func (a *Agent) trackSingleToolLoop(calls []llm.ToolCall, st *runState) {
	if len(calls) != 1 {
		st.lastSingleTool = ""
		st.consecutiveSameToolCount = 0
		return
	}
	name := calls[0].Name
	if name == st.lastSingleTool {
		st.consecutiveSameToolCount++
		return
	}
	st.lastSingleTool = name
	st.consecutiveSameToolCount = 1
}

// budgetWarning returns a one-time nudge when usage crosses a new context
// threshold, or "" if there's nothing new to report. *warned tracks the
// highest percentage already warned about, so crossing 75% doesn't nag
// every single step afterward — only the next, higher threshold matters.
// effectiveMaxTokens caps Config.MaxTokens against the room actually left
// in the context window, using the prompt size from the previous call as
// a stand-in for "about how big the next prompt will be" (history only
// grows a little per step, so this is a safe approximation, not the exact
// next value). Without this, asking for e.g. 32768 tokens of generation
// when the prompt itself is already using real context space causes
// exactly what koboldcpp warns about: "most of the context will be
// removed" — the backend starts evicting the prompt mid-generation and
// produces incoherent, runaway output instead of erroring cleanly.
func (a *Agent) effectiveMaxTokens(lastPromptTokens int) int {
	if a.cfg.ContextLimit <= 0 {
		return a.cfg.MaxTokens
	}
	promptEstimate := lastPromptTokens
	if promptEstimate <= 0 {
		// First call, nothing measured yet. System prompt + tool schemas
		// alone are routinely 1000+ tokens (we've seen 1135 in practice)
		// — don't assume zero just because we haven't measured it.
		promptEstimate = 1536
	}
	const safetyMargin = 256 // chat template / role overhead, not exact
	room := a.cfg.ContextLimit - promptEstimate - safetyMargin
	if room < 256 {
		room = 256 // always ask for *something* rather than zero/negative
	}
	if room > a.cfg.MaxTokens {
		return a.cfg.MaxTokens
	}
	return room
}

func (a *Agent) budgetWarning(usage llm.Usage, warned *int) string {
	if a.cfg.ContextLimit <= 0 || usage.PromptTokens <= 0 {
		return ""
	}
	pct := usage.PromptTokens * 100 / a.cfg.ContextLimit
	switch {
	case pct >= 90 && *warned < 90:
		*warned = 90
		return fmt.Sprintf(prompts.BudgetWarning, usage.PromptTokens, a.cfg.ContextLimit, pct)
	case pct >= 75 && *warned < 75:
		*warned = 75
		return fmt.Sprintf(prompts.BudgetNotice, usage.PromptTokens, a.cfg.ContextLimit, pct)
	default:
		return ""
	}
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
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
