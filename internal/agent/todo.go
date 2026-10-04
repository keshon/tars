package agent

import (
	"fmt"
	"strings"
)

// maxTodoBounces bounds how many text-only finishes with unchecked todos
// are bounced back into the run: a model that only narrates must still
// terminate. Intervening tool work resets the latch, so a working run
// never notices it. Mirrors MaxZeroWriteRefusals — same shape (cheap
// in-context retries), same number.
const maxTodoBounces = 3

// todoStepsPerItem funds acknowledged todo work: open items at first
// sight (cold-start for long task lists) and newly completed items
// afterwards, each worth this many steps. Total granted never exceeds
// one extra base budget — worst case is a doubled run. The audit that
// motivated this needed ~5 steps per item against a fixed 25.
const todoStepsPerItem = 4

// todoClosingSteps funds the delivery handshake when the list flips to
// all-done: worst case is a verify round plus the confirming report.
// Cap-exempt on purpose — the cap bounds work, and the handshake isn't
// work. A run that did everything right must never die mid-report.
const todoClosingSteps = 2

// checkTodoClosing reports whether this poll closes an episode: the list
// is empty having previously held items, and no handshake was granted
// since. Reopened items reset the latch. Pure state transition — the
// caller grants, updates the report, and tells the model.
func checkTodoClosing(tools *Registry, st *runState) bool {
	_, open := tools.TodoProgress()
	if len(open) > 0 {
		st.todoClosingGranted = false
		return false
	}
	if st.todoOpenMax == 0 || st.todoClosingGranted {
		return false
	}
	st.todoClosingGranted = true
	return true
}

// fundTodoSteps grants step budget for acknowledged checklist work,
// polled after each tool step. Creation funds cold-start (a fresh
// 5-item list immediately earns room); completions fund the tail.
// High-water marks make rewrites safe: deleting items doesn't refund,
// re-adding past the mark doesn't re-grant. Farming check-offs buys at
// most the capped extension — steps buy chance, never correctness,
// which stays governed by checks, verify, and stuck escalation.
func fundTodoSteps(tools *Registry, maxSteps int, st *runState) {
	if maxSteps <= 0 {
		return
	}
	done, open := tools.TodoProgress()
	grant := 0
	if len(open) > st.todoOpenMax {
		grant += (len(open) - st.todoOpenMax) * todoStepsPerItem
		st.todoOpenMax = len(open)
	}
	if done > st.todoDoneMax {
		grant += (done - st.todoDoneMax) * todoStepsPerItem
		st.todoDoneMax = done
	}
	if grant <= 0 {
		return
	}
	if room := maxSteps - st.todoFunded; room <= 0 {
		return
	} else if grant > room {
		grant = room
	}
	st.todoFunded += grant
}

// formatTodoOpen renders open todo texts for the finish-gate bounce:
// a short quoted list, never the whole backlog. Bounded output — this
// string itself lives in context.
func formatTodoOpen(open []string) string {
	const maxShown = 5
	shown := open
	more := ""
	if len(open) > maxShown {
		shown = open[:maxShown]
		more = fmt.Sprintf("\n(+%d more)", len(open)-maxShown)
	}
	var b strings.Builder
	for _, t := range shown {
		fmt.Fprintf(&b, "\n- %s", t)
	}
	return strings.TrimPrefix(b.String(), "\n") + more
}
