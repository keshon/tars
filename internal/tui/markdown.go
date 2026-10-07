package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// markdown-lite: headers, bold, inline code, lists, fences for
// answer-grade text, including simple pipe tables. Links, quotes, nested
// formatting, and escaped table pipes remain literal or unsupported.
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
	lines := strings.Split(raw, "\n")
	var out []mdLine
	inFence := false
	for i := 0; i < len(lines); i++ {
		line := lines[i]
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
		// Tables need a header row, a separator row, and | cells:
		// strict enough that shell pipelines never qualify.
		if strings.HasPrefix(trimmed, "|") && i+1 < len(lines) && isTableSep(lines[i+1]) {
			j := i + 2
			for j < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[j]), "|") {
				j++
			}
			out = append(out, renderTable(lines[i:j])...)
			i = j - 1
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

// isTableSep recognizes a | --- | :---: | row: cells of dashes and
// colons only, each with at least one dash.
func isTableSep(line string) bool {
	t := strings.Trim(strings.TrimSpace(line), "|")
	if t == "" {
		return false
	}
	for _, part := range strings.Split(t, "|") {
		p := strings.TrimSpace(part)
		if p == "" {
			return false
		}
		hasDash := false
		for _, r := range p {
			if r != '-' && r != ':' {
				return false
			}
			if r == '-' {
				hasDash = true
			}
		}
		if !hasDash {
			return false
		}
	}
	return true
}

type alignDir int

const (
	alignLeft alignDir = iota
	alignRight
	alignCenter
)

// splitTableRow cuts "| a | b |" into cells. Escaped pipes are out of
// scope (documented): a cell containing \| splits early, visibly.
func splitTableRow(line string) []string {
	t := strings.TrimSpace(line)
	t = strings.TrimPrefix(t, "|")
	t = strings.TrimSuffix(t, "|")
	parts := strings.Split(t, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// stripMd removes span markers for width math: columns align on
// visible text (markers vanish later, so measuring them would pad
// every marked cell a few columns too wide).
func stripMd(s string) string {
	s = strings.ReplaceAll(s, "**", "")
	return strings.ReplaceAll(s, "`", "")
}

func parseTableAlign(sep string, ncol int) []alignDir {
	cells := splitTableRow(sep)
	out := make([]alignDir, ncol)
	for i := range out {
		c := ""
		if i < len(cells) {
			c = strings.TrimSpace(cells[i])
		}
		switch {
		case strings.HasPrefix(c, ":") && strings.HasSuffix(c, ":") && len(c) > 1:
			out[i] = alignCenter
		case strings.HasSuffix(c, ":"):
			out[i] = alignRight
		default:
			out[i] = alignLeft
		}
	}
	return out
}

func padCell(s string, w int, a alignDir) string {
	n := lipgloss.Width(stripMd(s))
	if n >= w {
		return s
	}
	switch a {
	case alignRight:
		return strings.Repeat(" ", w-n) + s
	case alignCenter:
		left := (w - n) / 2
		return strings.Repeat(" ", left) + s + strings.Repeat(" ", w-n-left)
	default:
		return s + strings.Repeat(" ", w-n)
	}
}

// renderTable lays a | grid | out: padded cells, bold header, dim rule
// under it (fence-kind: dim, no spans). Wide tables overflow narrow
// terminals (reflow hard-cuts mid-row, rune-safe) — v1 accepts ragged
// edges over reflowing inside cells, which would break columns worse.
func renderTable(rows []string) []mdLine {
	header := splitTableRow(rows[0])
	ncol := len(header)
	aligns := parseTableAlign(rows[1], ncol)
	var body [][]string
	for _, r := range rows[2:] {
		body = append(body, splitTableRow(r))
	}
	widths := make([]int, ncol)
	measure := func(cells []string) {
		for i := 0; i < ncol && i < len(cells); i++ {
			if n := lipgloss.Width(stripMd(cells[i])); n > widths[i] {
				widths[i] = n
			}
		}
	}
	measure(header)
	for _, r := range body {
		measure(r)
	}
	join := func(cells []string) string {
		for len(cells) < ncol {
			cells = append(cells, "")
		}
		padded := make([]string, ncol)
		for i := range padded {
			padded[i] = padCell(cells[i], widths[i], aligns[i])
		}
		return "| " + strings.Join(padded, " | ") + " |"
	}
	var out []mdLine
	out = append(out, mdLine{text: join(header), header: true})
	rule := make([]string, ncol)
	for i := range rule {
		rule[i] = strings.Repeat("─", max(widths[i], 3))
	}
	out = append(out, mdLine{text: join(rule), fence: true})
	for _, r := range body {
		out = append(out, mdLine{text: join(r)})
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
