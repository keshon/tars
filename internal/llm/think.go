package llm

import (
	"strings"
	"unicode/utf8"
)

const (
	thinkOpen  = "<think>"
	thinkClose = "</think>"
)

// ThinkRegions returns byte ranges of <think>...</think> blocks in
// content. A trailing unclosed <think> runs to end of message (the
// generation was cut, not finished). Tags are matched exactly lowercase:
// that is what Qwen emits and what the koboldcpp content grammar allows
// through, so anything else is prose about thinking, not thinking.
func ThinkRegions(content string) [][2]int {
	var out [][2]int
	pos := 0
	for {
		open := strings.Index(content[pos:], thinkOpen)
		if open < 0 {
			return out
		}
		inner := pos + open + len(thinkOpen)
		close := strings.Index(content[inner:], thinkClose)
		if close < 0 {
			out = append(out, [2]int{pos + open, len(content)})
			return out
		}
		out = append(out, [2]int{pos + open, inner + close + len(thinkClose)})
		pos = inner + close + len(thinkClose)
	}
}

// ThinkChars counts runes inside think regions: the reasoning budget is
// measured in characters because no backend on the wire reports reasoning
// tokens separately (verified: koboldcpp usage has prompt/completion/total
// only). Chars/4 approximates tokens, the same estimate budgets use
// elsewhere for unmeasured text.
func ThinkChars(content string) int {
	n := 0
	for _, r := range ThinkRegions(content) {
		inner := content[r[0]+len(thinkOpen) : r[1]]
		inner = strings.TrimSuffix(inner, thinkClose)
		n += utf8.RuneCountInString(inner)
	}
	return n
}

// inRegions reports whether byte offset p falls inside any region.
func inRegions(p int, regions [][2]int) bool {
	for _, r := range regions {
		if p >= r[0] && p < r[1] {
			return true
		}
	}
	return false
}

// DeliberationChars measures everything the model spent reasoning on
// this turn, both channels: inline <think> blocks (koboldcpp) and the
// separate reasoning field (llama.cpp). Double counting when a backend
// reports both is accepted: it only makes wrap-ups more eager, and
// MaxThinkWraps bounds the consequence.
func DeliberationChars(m Message) int {
	return ThinkChars(m.Content) + utf8.RuneCountInString(m.Reasoning)
}

// stripRegions returns content with the given byte ranges removed.
func stripRegions(content string, regions [][2]int) string {
	if len(regions) == 0 {
		return content
	}
	var b strings.Builder
	prev := 0
	for _, r := range regions {
		b.WriteString(content[prev:r[0]])
		prev = r[1]
	}
	b.WriteString(content[prev:])
	return b.String()
}
