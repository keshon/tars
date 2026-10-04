package agent

import (
	"github.com/keshon/tars/internal/llm"
	"github.com/keshon/tars/internal/prompts"
)

// stepOutcome carries the parts of a completed step that interject can't
// read off runState.
type stepOutcome struct {
	// repeat is true when this step's tool calls were byte-identical to
	// the previous step's — it picks StuckRepeating over StuckFailing.
	repeat bool

	// budget is a context-usage notice for this step, or "" for none.
	budget string
}

// markNudge reports harness text and returns it marked, for call sites
// (interject) that append history themselves.
func (a *Agent) markNudge(kind, text string) string {
	marked := harnessText(text)
	a.notifyNudge(kind, marked)
	return marked
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
		msg := prompts.SearchFatigue
		if a.hasTool("ask_user") {
			msg += " " + prompts.AskHint
		}
		return a.markNudge(NudgeStuck, msg)
	}

	msg := st.pendingBudget
	st.pendingBudget = ""
	if msg != "" {
		return a.markNudge(NudgeBudget, msg)
	}
	return msg
}

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
	NudgeTodo      = "todo"
	NudgeClosing   = "closing"
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
