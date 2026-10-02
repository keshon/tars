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
	out = append(out, llm.Message{Role: llm.RoleUser, Content: compactNotice(dropped)})
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
