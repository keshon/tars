package agent

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/keshon/tars/internal/llm"
	"github.com/keshon/tars/internal/prompts"
)

// finishOutcome is what one text-only turn resolves to: either the run
// continues (done false, history grown by a nudge) or it ends with an
// answer or an error. Specific to finish handling — not a generic
// outcome framework.
type finishOutcome struct {
	done    bool
	answer  string
	err     error
	history []llm.Message
}

// handleFinish decides what a model turn without tool calls means: a
// recoverable truncation, a ramble over budget, leaked-call residue, a
// blocked verify, an unchecked todo list, the verify round, a refused
// zero-write finish, an empty finish, or a genuine answer. Check order
// is load-bearing (verify catches the first announcement, the refusal
// catches the rest) — preserve it.
func (a *Agent) handleFinish(ctx context.Context, st *runState, step int, resp llm.ChatResponse, leakedText bool, budgetNudge string, history []llm.Message) finishOutcome {
	// finish_reason "length" means the backend cut generation off
	// and discarded whatever the model was building — usually the
	// tool call it had just announced. That's a truncation, never a
	// finish: treating the stump as an answer is how a live run
	// "finished" a subtask with "Let me write the plan.md file…"
	// and zero writes. Nudge and continue; escalate via the stuck
	// counter so a backend that truncates every response is still
	// bounded by MaxStuckSteps → MaxSteps.
	if resp.FinishReason == "length" {
		msg := prompts.Truncated
		if a.hasTool("write_file") {
			msg += " " + prompts.WriteHint
		}
		history = a.nudge(history, NudgeTruncated, msg)
		st.stuckSteps++
		if st.stuckSteps >= a.cfg.MaxStuckSteps {
			history = a.nudge(history, NudgeStuck, prompts.StuckFailing)
			st.stuckSteps = 0
		}
		return finishOutcome{history: history}
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
			return finishOutcome{history: history}
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
		return finishOutcome{history: history}
	}

	if st.blockFinishDueToVerify {
		st.blockFinishDueToVerify = false
		st.verifiedOnce = false
		history = a.nudge(history, NudgeVerify, fmt.Sprintf(prompts.VerifyFailedContinue, st.verifyFailedOutput))
		st.verifyFailedOutput = ""
		return finishOutcome{history: history}
	}

	// A text reply with unchecked todos is a progress report, not
	// a finish: quote the open items back and continue the run.
	// No todo tool (or an empty list) means nothing pending, so
	// chit-chat is untouched. The latch keeps narration-only
	// loops terminal; intervening tool work resets it below.
	if _, open := a.cfg.Tools.TodoProgress(); len(open) > 0 && st.todoBounces < maxTodoBounces {
		st.todoBounces++
		history = a.nudge(history, NudgeTodo, fmt.Sprintf(prompts.TodoOpen, len(open), formatTodoOpen(open)))
		a.saveState(history)
		return finishOutcome{history: history}
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
		verifyMsg := prompts.Verify + a.stepTag(st, step)
		// The zero-writes fact is evidence of failure only where
		// writes were expected (workers told to write) or the
		// answer itself claims file effects (fake-save shape).
		// On a read-only run it reads as an accusation and a weak
		// model "fixes" it by writing an unasked file.
		if st.mutatingSucceeded == 0 &&
			(a.cfg.VerifyOnZeroWrites || claimsFileEffects(resp.Message.Content)) {
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
		return finishOutcome{history: history}
	}
	// Announcing the work is not doing the work. A worker that ends
	// its turn with "Now let me write the plan.md file" and no tool
	// call has produced nothing, and its own narration is the only
	// evidence to the contrary. The verify round above catches the
	// FIRST such finish — but only the first, because verifiedOnce
	// is a one-shot. Live failure, parseurl mission: the worker announced, took the verify nudge, announced again,
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
				strings.Join(a.cfg.MutatingTools, "/"))+a.stepTag(st, step))
			a.saveState(history)
			return finishOutcome{history: history}
		}
		// Refusals exhausted and still nothing written: end the
		// run as a failure, not a narration-shaped success.
		// Wrapping ErrMaxSteps keeps mission retry semantics (a
		// stuck worker can converge; a dead backend cannot).
		return finishOutcome{done: true, err: fmt.Errorf("%w: announced completion %d times without writing anything",
			ErrMaxSteps, st.zeroWriteFinishes)}
	}
	if strings.TrimSpace(resp.Message.Content) == "" {
		if !st.emptyFinishRetried {
			st.emptyFinishRetried = true
			return finishOutcome{history: history}
		}
		return finishOutcome{done: true, err: fmt.Errorf("%w: empty finish after retry", ErrMaxSteps)}
	}
	a.LastRunMutations = st.mutatingSucceeded
	a.report.MutatedPaths = st.mutatedPaths
	a.report.Final = resp.Message.Content
	return finishOutcome{done: true, answer: resp.Message.Content, history: history}
}

// wrapUpTurn issues one final text-only turn when the step budget is
// exhausted: the model summarizes what was accomplished, what remains,
// and the most useful next step. Tools stay in the request (opencode
// parity — measure before hardening); any calls made are ignored, only
// text is kept. Not a step: no hooks fire, no counters move, history is
// untouched. Returns false when there is nothing to close (zero steps
// ran) or the call itself fails — the original error stands either way.
func (a *Agent) wrapUpTurn(ctx context.Context, st *runState, history []llm.Message) (string, bool) {
	if a.report.Steps <= 0 {
		return "", false
	}
	h := append(history, llm.Message{Role: llm.RoleUser, Content: prompts.WrapUp})
	resp, err := a.chat(ctx, llm.ChatRequest{
		Messages:  h,
		Tools:     a.cfg.Tools.Defs(),
		MaxTokens: a.effectiveMaxTokens(st.lastPromptTokens),
	})
	if err != nil {
		return "", false
	}
	if strings.TrimSpace(resp.Message.Content) == "" {
		return "", false
	}
	return resp.Message.Content, true
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
