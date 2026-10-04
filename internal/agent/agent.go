package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/keshon/tars/internal/llm"
	"github.com/keshon/tars/internal/session"
)

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

	// MaxSteps is the step budget enforced this run: base plus
	// todo-funded extension, kept live so observers (step events, status
	// meters) read the enforced line, not the configured one.
	MaxSteps int

	// OpenTodos is the checklist still unchecked when the run ended
	// (nil when none). Snapshot on the MaxSteps path only — that is the
	// one exit where unfinished acknowledged work needs an operator
	// decision. Sessions read it to ask for one more budget.
	OpenTodos []string

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

type runState struct {
	stuckSteps, exploratorySteps, mutatingSucceeded, warnedThreshold, lastPromptTokens int
	consecutiveSameToolCount                                                           int
	// warnedSteps tracks the highest step-budget threshold already
	// warned about (75/90 of the enforced limit), mirroring
	// warnedThreshold for tokens.
	warnedSteps int
	// lastSingleResult fingerprints the previous same-tool step's
	// outcome, so the loop counter accumulates identical results, not
	// mere repetition: three fresh reads are exploration, three times
	// the same bytes are grinding. See trackSingleToolLoop.
	lastSingleResult                                                          uint64
	verifiedOnce, searchFatigueWarned, blockFinishDueToVerify, toolLoopWarned bool
	emptyFinishRetried                                                        bool
	lastSignature, verifyFailedOutput, lastSingleTool                         string
	mutatedPaths                                                              []string

	// zeroWriteFinishes counts how many times the model has tried to end
	// the run having written nothing, while VerifyOnZeroWrites says the
	// run was supposed to write. See MaxZeroWriteRefusals.
	zeroWriteFinishes int

	// todoBounces counts text-only finishes bounced for unchecked todos
	// since the last tool work. See maxTodoBounces.
	todoBounces int

	// todoFunded counts step budget granted for acknowledged todo work.
	// todoOpenMax and todoDoneMax are high-water marks (not snapshots):
	// rewrites neither double-fund nor un-complete — deleting an item
	// doesn't refund, re-adding it past the mark doesn't re-grant.
	todoFunded  int
	todoOpenMax int
	todoDoneMax int
	// todoClosingGranted latches the delivery handshake per all-done
	// episode: reopening items resets it, so each genuine completion
	// earns its own close.
	todoClosingGranted bool

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

// ErrMaxSteps marks a run that did real work but ran out of step budget —
// as opposed to infrastructure failures (a dead backend, a network error)
// where no work happened at all. Callers that retry on failure (the
// mission fix loop) must distinguish the two: retrying a step-budget
// death can converge; retrying against a dead backend just burns retry
// budget on connection errors.
var ErrMaxSteps = errors.New("reached max steps")

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// hasTool reports whether this run's registry offers the named tool.
// Nudge composition consults it so role-agnostic notices never cite
// tools the recipient was never given (delegate_task to a worker,
// ask_user to a subagent). Nil-registry safe: unknown means absent.
func (a *Agent) hasTool(name string) bool {
	return a.cfg.Tools != nil && containsStr(a.cfg.Tools.Names(), name)
}
