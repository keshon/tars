package agent

import (
	"fmt"
	"sort"
	"strings"

	"github.com/keshon/tars/internal/llm"
	"github.com/keshon/tars/internal/prompts"
)

// compactHistory drops older assistant-led step groups from history while
// keeping the system prompt, original user task, and the most recent
// keepSteps groups intact. Tool results are never separated from their
// matching assistant tool_call. The inserted notice carries an extractive
// summary of what was dropped (step count, tools used, paths touched) so
// the model keeps ground facts instead of just being told text is gone.
func compactHistory(history []llm.Message, keepSteps int) []llm.Message {
	if keepSteps <= 0 || len(history) <= 2 {
		return history
	}

	prefix := append([]llm.Message(nil), history[:2]...)
	rest := history[2:]
	groups := stepGroups(rest)
	if len(groups) <= keepSteps {
		return history
	}

	cut := findCutPoint(groups, len(groups)-keepSteps)
	dropped := groups[:cut]
	kept := groups[cut:]

	out := append([]llm.Message(nil), prefix...)
	out = append(out, llm.Message{Role: llm.RoleUser, Content: harnessText(compactNotice(dropped))})
	for _, g := range kept {
		out = append(out, g...)
	}
	return out
}

// findCutPoint returns how many leading groups to drop so exactly want
// groups are dropped, adjusted so the cut never orphans a tool result:
// groups are assistant-led by construction, but a defensive scan backs
// the cut off if kept[0] would start with a non-assistant message.
func findCutPoint(groups [][]llm.Message, want int) int {
	if want < 0 {
		want = 0
	}
	if want > len(groups) {
		want = len(groups)
	}
	for want > 0 && want < len(groups) && len(groups[want]) > 0 &&
		groups[want][0].Role != llm.RoleAssistant {
		want--
	}
	return want
}

// compactNotice renders the insertion: the base notice plus a mechanical
// summary of dropped groups. Extractive on purpose — no model call, so it
// cannot fail and costs nothing on a weak backend.
func compactNotice(dropped [][]llm.Message) string {
	summary := summarizeDropped(dropped)
	if summary == "" {
		return prompts.CompactNotice
	}
	return prompts.CompactNotice + "\n" + summary
}

// stepGroups splits messages into assistant-led steps: each group starts
// with an assistant message and includes every following non-assistant
// message until the next assistant turn.
func stepGroups(msgs []llm.Message) [][]llm.Message {
	var groups [][]llm.Message
	i := 0
	for i < len(msgs) {
		start := i
		i++
		for i < len(msgs) && msgs[i].Role != llm.RoleAssistant {
			i++
		}
		groups = append(groups, msgs[start:i])
	}
	return groups
}

// pruneMinimumTokens stops compaction from firing on small windows: below
// this many prompt tokens there is nothing worth summarizing and the churn
// costs more than it saves.
const pruneMinimumTokens = 20000

// Context budgets: the token math that decides generation sizes and
// warning thresholds. Sibling to compaction (which reacts when the
// window fills) rather than part of it.

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

// budgetWarning returns a one-time nudge when usage crosses a new context
// threshold, or "" if there's nothing new to report. *warned tracks the
// highest percentage already warned about, so crossing 75% doesn't nag
// every single step afterward — only the next, higher threshold matters.
func (a *Agent) budgetWarning(usage llm.Usage, warned *int) string {
	if a.cfg.ContextLimit <= 0 || usage.PromptTokens <= 0 {
		return ""
	}
	pct := usage.PromptTokens * 100 / a.cfg.ContextLimit
	msg := ""
	switch {
	case pct >= 90 && *warned < 90:
		*warned = 90
		msg = fmt.Sprintf(prompts.BudgetWarning, usage.PromptTokens, a.cfg.ContextLimit, pct)
	case pct >= 75 && *warned < 75:
		*warned = 75
		msg = fmt.Sprintf(prompts.BudgetNotice, usage.PromptTokens, a.cfg.ContextLimit, pct)
	default:
		return ""
	}
	if a.hasTool("delegate_task") {
		msg += " " + prompts.DelegateHint
	}
	return msg
}

// defaultCompactAtPercent is the share of the context window at which
// history is compacted. Well below the old 90%: a window is what the
// backend will accept, not what the model can still reason over, and the
// steps taken between those two points are the ones that produce
// confidently wrong work.
const defaultCompactAtPercent = 60

// dropRepeatCaches forgets which calls ran and which failed. The refusal
// texts cite conversation content ("re-read it from the conversation",
// "already failed identically") — true while that content is present,
// false once compaction drops it or a mutation supersedes it. The maps
// re-learn from fresh executions, bounded exactly as before.
func dropRepeatCaches(st *runState) {
	st.idempotentSeen = nil
	st.failedCalls = nil
}

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
	dropRepeatCaches(st)
	return true
}

// summarizeDropped extracts ground facts from dropped step groups: how many
// steps, which tools ran, which paths were touched, and the last assistant
// text snippet. Bounded output — this string itself lives in context.
func summarizeDropped(dropped [][]llm.Message) string {
	if len(dropped) == 0 {
		return ""
	}
	toolCounts := map[string]int{}
	var paths []string
	lastText := ""
	for _, g := range dropped {
		for _, m := range g {
			if m.Role == llm.RoleAssistant {
				for _, tc := range m.ToolCalls {
					toolCounts[tc.Name]++
					for _, p := range mutatedPathsFromCall(tc.Arguments) {
						if p != "" && !containsStr(paths, p) && len(paths) < 20 {
							paths = append(paths, p)
						}
					}
				}
				if strings.TrimSpace(m.Content) != "" {
					lastText = strings.TrimSpace(m.Content)
				}
			}
			if m.Role == llm.RoleTool {
				// Delegate reports name no files in their call
				// arguments — the paths live in the result envelope.
				// Without this, subagent work compacts down to a bare
				// delegate_task×N with no record of what it touched.
				_, dps := parseDelegateMutations(m.Content)
				for _, p := range dps {
					if p != "" && !containsStr(paths, p) && len(paths) < 20 {
						paths = append(paths, p)
					}
				}
			}
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "(dropped %d earlier steps:", len(dropped))
	names := make([]string, 0, len(toolCounts))
	for n := range toolCounts {
		names = append(names, n)
	}
	sort.Strings(names)
	for i, n := range names {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, " %s×%d", n, toolCounts[n])
	}
	if len(paths) > 0 {
		fmt.Fprintf(&b, "; files: %s", strings.Join(paths, ", "))
	}
	b.WriteString(")")
	if lastText != "" {
		r := []rune(lastText)
		if len(r) > 200 {
			lastText = string(r[:200]) + "…"
		}
		fmt.Fprintf(&b, " Last note: %q", lastText)
	}
	return b.String()
}
