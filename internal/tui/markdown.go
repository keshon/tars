package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// markdown-lite: headers, bold, inline code, lists, fences for
// answer-grade text. Deliberately NOT a full renderer: no tables,
// links, quotes, or nesting. Rationale: the model emits all five
// constantly and nothing else often; a 100-line subset fixes ~70% of
// the rough-MVP feel without a glamour dependency (scope discipline).
//
// Applied to answer, user, and think roles only. Tool args/results,
// gates, and markers keep raw text: a glob like `*.go` must never
// toggle bold, and diff prefixes must survive styling.

// mdLine is one logical line plus its block kind. Fences cover markers
// and content alike: inside a fence nothing is interpreted.
type mdLine struct {
	text          string
	header, fence bool
}

// mdBlockLines splits raw text into interpreted lines: fence tracking,
// "# Head" (space required, so "#tag" stays literal), and "- "/"* "
// bullets prettified to "• " (same 2-cell cost, no reflow change).
func mdBlockLines(raw string) []mdLine {
	var out []mdLine
	inFence := false
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			out = append(out, mdLine{text: line, fence: true})
			continue
		}
		if inFence {
			out = append(out, mdLine{text: line, fence: true})
			continue
		}
		if text, ok := cutHeader(line); ok {
			out = append(out, mdLine{text: text, header: true})
			continue
		}
		if text, ok := cutBullet(line); ok {
			out = append(out, mdLine{text: text})
			continue
		}
		out = append(out, mdLine{text: line})
	}
	return out
}

// cutHeader strips 1-6 "#" followed by a space. "#tag" is literal.
func cutHeader(line string) (string, bool) {
	i := 0
	for i < len(line) && line[i] == '#' {
		i++
	}
	if i == 0 || i > 6 || i >= len(line) || line[i] != ' ' {
		return "", false
	}
	return strings.TrimSpace(line[i:]), true
}

// cutBullet prettifies "- "/"* " list markers, keeping indentation.
func cutBullet(line string) (string, bool) {
	i := 0
	for i < len(line) && line[i] == ' ' {
		i++
	}
	rest := line[i:]
	if strings.HasPrefix(rest, "- ") || strings.HasPrefix(rest, "* ") {
		return line[:i] + "• " + rest[2:], true
	}
	return "", false
}

// spanState tracks open **bold** / `code` spans. Fresh per logical
// line (see renderSpans): a wrap-split span survives fragments, while
// a stray marker can only stain one line, never the block.
type spanState struct{ bold, code bool }

// baseStyle returns the role's plain style; span styles derive from it
// so every segment carries its full style and ANSI never nests.
func baseStyle(r role, st styles) lipgloss.Style {
	switch r {
	case roleUser:
		return st.user
	case roleThink:
		return st.think
	case roleMarker:
		return st.dim
	default:
		return lipgloss.Style{}
	}
}

// renderSpans parses **bold** and `code` in plain text, emitting
// independently styled segments joined plain. Markers are ASCII, so
// byte scanning never splits a rune. An unclosed marker is consumed
// (toggle): bounded to its line by the fresh-per-line state.
func renderSpans(frag string, base, bold, code lipgloss.Style, s *spanState) string {
	var sb strings.Builder
	var seg strings.Builder
	flush := func() {
		style := base
		switch {
		case s.code:
			style = code
		case s.bold:
			style = bold
		}
		sb.WriteString(style.Render(seg.String()))
		seg.Reset()
	}
	i := 0
	for i < len(frag) {
		if strings.HasPrefix(frag[i:], "**") {
			flush()
			s.bold = !s.bold
			i += 2
			continue
		}
		if frag[i] == '`' {
			flush()
			s.code = !s.code
			i++
			continue
		}
		seg.WriteByte(frag[i])
		i++
	}
	flush()
	return sb.String()
}
