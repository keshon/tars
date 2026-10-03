package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// thinkToggleHint names the thinking expand/collapse key in UI text.
// The binding lives in handleKey; both stay in sync through this.
const thinkToggleHint = "ctrl+g"

// role names the kind of a transcript block. The renderer maps every
// role to a style + gutter, so unstyled text in the transcript is a
// bug, never a default (R2).
type role int

const (
	roleAnswer role = iota
	roleUser
	roleThink
	roleTool
	roleResult
	roleGate
	roleMission
	roleMarker
	roleError
	// roleWarning is a deterministic-check finding (gofmt, secrets):
	// report-only signal, never an error. Yellow, unbold (gate yellow
	// is bold) with a "» " rail.
	roleWarning
)

// block is one transcript entry: raw content plus its role. Styling
// happens at render time, so toggles (think expand) re-render without
// touching history.
type block struct {
	role role
	text string
	// at stamps when the block entered the transcript. Stamped roles
	// render it right-aligned (opencode's metadata-right pattern);
	// zero means "unstamped". See stamps().
	at time.Time
	// bare skips the per-line role gutter: section titles carry a
	// reference block's identity, so gutters would only restripe it
	// into fake bullets. Amends R6 (plan TUI-19).
	bare bool
	// breakBefore opens a turn: a faint rule renders above the block
	// (user echoes, new-task markers), grouping the transcript into
	// turns instead of a flat log.
	breakBefore bool
	// Tool card fields (roleTool only): callID pairs the result event,
	// result/output text lands on attach, open until it does, failed
	// when the result reports an error.
	callID string
	result string
	open   bool
	failed bool
}

func answerBlock(s string) block { return block{role: roleAnswer, text: s, at: time.Now()} }
func userBlock(s string) block {
	// Every user echo opens a turn. The one exception (reject-note
	// echo, mid-turn) clears the flag at its call site.
	return block{role: roleUser, text: s, at: time.Now(), breakBefore: true}
}
func thinkBlock(s string) block   { return block{role: roleThink, text: s, at: time.Now()} }
func resultBlock(s string) block  { return block{role: roleResult, text: s, at: time.Now()} }
func gateBlock(s string) block    { return block{role: roleGate, text: s, at: time.Now()} }
func missionBlock(s string) block { return block{role: roleMission, text: s, at: time.Now()} }
func markerBlock(s string) block  { return block{role: roleMarker, text: s, at: time.Now()} }
func errorBlock(s string) block   { return block{role: roleError, text: s, at: time.Now()} }

// findingBlock renders one deterministic-check finding as plain text;
// the rule carries the meaning, the warning style the urgency.
func findingBlock(rule, path string, line int, summary string) block {
	text := "check " + rule
	if path != "" {
		text += fmt.Sprintf(" %s:%d", path, line)
	}
	return block{role: roleWarning, text: text + " " + summary, at: time.Now()}
}

// toolCardBlock opens a tool card: the call header renders immediately,
// the result attaches when its tool_result event lands (matched by ID
// in attachResult). Text carries no arrow prefix: the role gutter
// already marks tool lines.
func toolCardBlock(callID, text string) block {
	return block{role: roleTool, text: text, at: time.Now(), callID: callID, open: true}
}

// attachResult lands a tool result on its card: the most recent open
// card with a matching non-empty call ID. Unknown IDs (and empty ones)
// fall back to a standalone result block so parallel or leaked results
// can never corrupt the wrong card. Reports whether it attached.
func (m *model) attachResult(callID, text string) bool {
	if callID == "" {
		return false
	}
	for i := len(m.blocks) - 1; i >= 0; i-- {
		b := &m.blocks[i]
		if b.role == roleTool && b.open && b.callID == callID {
			b.result = text
			b.open = false
			b.failed = strings.HasPrefix(text, "error:")
			m.refreshContent()
			return true
		}
	}
	return false
}

// gutterWidth is fixed so future wrap math can count it (R6).
const gutterWidth = 2

// gutterGlyph is the role's unstyled 2-cell left marker. Kept separate
// from gutter so tests can assert the width without ANSI involved.
func gutterGlyph(r role) string {
	switch r {
	case roleUser:
		return "> "
	case roleThink:
		return "~ "
	case roleTool:
		return "→ "
	case roleGate:
		return "? "
	case roleMission:
		return "# "
	case roleMarker:
		return "- "
	case roleError:
		return "! "
	case roleWarning:
		return "» "
	default:
		return "  "
	}
}

// gutter colors the role's marker: color carries the meaning, the
// fixed-width glyph keeps every block aligned.
func (st styles) gutter(r role) string {
	color := st.dim
	switch r {
	case roleUser:
		color = st.user
	case roleThink:
		color = st.think
	case roleGate:
		color = st.gate
	case roleError:
		color = st.err
	case roleWarning:
		color = st.warn
	}
	return color.Render(gutterGlyph(r))
}

// thinkPreviewLen caps the collapsed thinking summary (runes, not bytes).
const thinkPreviewLen = 80

// thinkSummary shrinks reasoning to one line plus its size: the reader
// sees that thinking happened and how much, never the raw
// chain-of-thought as answer text.
func thinkSummary(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	if runes := []rune(line); len(runes) > thinkPreviewLen {
		line = string(runes[:thinkPreviewLen]) + "…"
	}
	return fmt.Sprintf("%s [%d chars]", line, len([]rune(s)))
}

// renderBlock styles one block and prefixes its role gutter on every
// line. The expand flag (inverse of compact view) opens verbose
// content: latest thinking in full, long tool results in full.
// Thinking renders collapsed unless expanded. Answer, user, and
// think roles run the markdown-lite path (fences/headers/spans);
// tool args, diffs, gates, and markers stay raw so a glob `*.go` can
// never toggle bold and diff prefixes survive styling. Lines reflow
// to width columns (P10-6): the viewport never wraps, so unbroken long
// lines would vanish past the right edge. Continuation fragments take
// a blank gutter, marking them as wrapped rather than new.
func renderBlock(b block, st styles, expandThink bool, width int) string {
	raw := b.text
	if b.role == roleThink {
		if !expandThink {
			raw = "⋯ " + thinkSummary(b.text) + " (" + thinkToggleHint + " for full view)"
		} else {
			// Framed, never capped: the header orients a long
			// expansion without hiding any of it.
			raw = fmt.Sprintf("── thinking · %d chars · %s for compact view ──\n%s",
				len([]rune(b.text)), thinkToggleHint, b.text)
		}
	}
	stamped := !b.at.IsZero() && stamps(b.role)
	var out []string
	switch {
	case b.role == roleTool:
		out = renderToolCard(b, st, expandThink, stamped, width)
	case isMdRole(b.role):
		out = renderMdBlock(b, raw, st, stamped, width)
	default:
		logical := strings.Split(raw, "\n")
		for li, line := range logical {
			for i, f := range reflow(line, fragWidth(width, stamped, li == len(logical)-1)) {
				g := ""
				if !b.bare {
					g = st.gutter(b.role)
					if i > 0 {
						g = "  "
					}
				}
				out = append(out, g+applyRoleStyle(b.role, line, f, st))
			}
		}
	}
	if stamped {
		out[len(out)-1] = alignStamp(out[len(out)-1], st, b.at, width)
	}
	return strings.Join(out, "\n")
}

// resultPreviewLines caps collapsed tool results (pi parity:
// FALLBACK_PREVIEW_LINES=10). The overflow note keeps the count, so
// compacted output never silently loses lines.
const resultPreviewLines = 10

// renderToolCard draws one bordered-by-rail tool card: a header (status
// glyph + call) plus attached result lines, or a pending rail while
// open. The "│ " rail groups call with result without full-border
// width math; continuations keep the rail so the card edge never
// breaks (overriding the blank-gutter rule inside cards). Compact view
// previews long results; full view shows all.
func renderToolCard(b block, st styles, expand, stamped bool, width int) []string {
	glyph := st.add.Render("✓")
	if b.open {
		glyph = st.dim.Render("…")
	} else if b.failed {
		glyph = st.err.Render("✕")
	}
	var out []string
	out = append(out, st.gutter(b.role)+glyph+" "+st.dim.Render(b.text))
	if b.open {
		out = append(out, "  "+st.dim.Render("│ …"))
		return out
	}
	lines := strings.Split(b.result, "\n")
	if !expand && len(lines) > resultPreviewLines {
		kept := lines[:resultPreviewLines]
		kept = append(kept, fmt.Sprintf("… (%d more lines)", len(lines)-resultPreviewLines))
		lines = kept
	}
	for li, line := range lines {
		w := width - gutterWidth - 2
		if stamped && li == len(lines)-1 {
			w -= stampWidth
		}
		for _, f := range reflow(line, w) {
			out = append(out, "  "+st.dim.Render("│ ")+resultLineStyle(line, f, st))
		}
	}
	return out
}

// fragWidth reserves the gutter on every line and the timestamp on the
// block's last line, so neither pushes past the edge.
func fragWidth(width int, stamped, last bool) int {
	w := width - gutterWidth
	if stamped && last {
		w -= stampWidth
	}
	return w
}

// isMdRole reports whether the role renders markdown-lite. Tool args
// (`*.go` globs), diffs, and gate prompts are deliberately excluded;
// markers (help, status) render so their structure shows.
func isMdRole(r role) bool {
	return r == roleAnswer || r == roleUser || r == roleThink || r == roleMarker
}

// renderMdBlock interprets fences/headers/spans, then reflows and
// styles. Span state is fresh per logical line: a wrap-split span
// survives fragments, a stray marker stains one line at most.
func renderMdBlock(b block, raw string, st styles, stamped bool, width int) []string {
	base := baseStyle(b.role, st)
	var out []string
	mls := mdBlockLines(raw)
	for li, ml := range mls {
		span := &spanState{}
		for i, f := range reflow(ml.text, fragWidth(width, stamped, li == len(mls)-1)) {
			var body string
			if ml.fence {
				body = st.dim.Render(f)
			} else {
				bb := base
				if ml.header {
					bb = base.Bold(true)
				}
				body = renderSpans(f, bb, bb.Bold(true), bb.Foreground(lipgloss.Color("6")), span)
			}
			g := ""
			if !b.bare {
				g = st.gutter(b.role)
				if i > 0 {
					g = "  "
				}
			}
			out = append(out, g+body)
		}
	}
	return out
}

// stampWidth reserves room for the " [15:04]" suffix when wrapping the
// block's last line, so the stamp never pushes it past the edge.
const stampWidth = 8

// alignStamp appends the timestamp right-aligned to width: content
// left, time as a clean right column. Padding sits between content and
// stamp, so no line ever ends in whitespace.
func alignStamp(line string, st styles, at time.Time, width int) string {
	stamp := st.dim.Render(" [" + at.Format("15:04") + "]")
	pad := width - lipgloss.Width(line) - stampWidth
	if pad < 1 {
		pad = 1
	}
	return line + strings.Repeat(" ", pad) + stamp
}

// applyRoleStyle colors one fragment for the raw (non-markdown) roles.
// Answer/user/think/marker never reach it: they render through renderMdBlock.
func applyRoleStyle(r role, origLine, frag string, st styles) string {
	switch r {
	case roleTool:
		return st.dim.Render(frag)
	case roleResult:
		return resultLineStyle(origLine, frag, st)
	case roleGate:
		return st.gate.Render(frag)
	case roleMission:
		return st.dim.Render(frag)
	case roleMarker:
		return st.dim.Render(frag)
	case roleError:
		return st.err.Render(frag)
	case roleWarning:
		return st.warn.Render(frag)
	default:
		return frag
	}
}

// resultLineStyle colors one fragment by its logical line's diff prefix.
func resultLineStyle(origLine, frag string, st styles) string {
	switch {
	case strings.HasPrefix(origLine, "+") && !strings.HasPrefix(origLine, "++"):
		return st.add.Render(frag)
	case strings.HasPrefix(origLine, "-") && !strings.HasPrefix(origLine, "--"):
		return st.del.Render(frag)
	case strings.HasPrefix(origLine, "@@"):
		return st.hunk.Render(frag)
	default:
		return st.dim.Render(frag)
	}
}

// appendBlock records a block and re-renders the transcript.
func (m *model) appendBlock(b block) {
	m.blocks = append(m.blocks, b)
	m.refreshContent()
}

// stamps reports whether the role carries a timestamp. Conversation
// content, decisions, and failures do; machine chrome (tool/result
// calls, markers, mission notes) stays clean — their timing never
// answers a reader's question.
func stamps(r role) bool {
	switch r {
	case roleUser, roleAnswer, roleThink, roleGate, roleError:
		return true
	}
	return false
}

// turnRule is a full-width dim divider opening a turn.
func turnRule(width int, st styles) string {
	if width < minWrapWidth {
		width = minWrapWidth
	}
	return st.dim.Render(strings.Repeat("─", width))
}

// refreshContent re-renders every block into the viewport. Called on
// append, on toggles, and on resize (reflow follows the width). YOffset
// survives SetContent, so a reading user is never yanked (pinned by
// TestNewBlocksRespectUnfollow).
func (m *model) refreshContent() {
	if !m.ready {
		return
	}
	var sb strings.Builder
	// Only the latest think block follows the toggle: expanding
	// meant reading current reasoning, not flooding history. Older
	// thinks stay summarized until a newer one arrives.
	lastThink := -1
	for i, b := range m.blocks {
		if b.role == roleThink {
			lastThink = i
		}
	}
	for i, b := range m.blocks {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		// Turn separators: a turn-opening block draws the rule above
		// it, unless it opens the transcript or follows another
		// opener (new-task marker into first echo).
		if b.breakBefore && i > 0 && !m.blocks[i-1].breakBefore {
			sb.WriteString(turnRule(m.vp.Width, m.styles) + "\n\n")
		}
		sb.WriteString(renderBlock(b, m.styles, !m.compact && (b.role != roleThink || i == lastThink), m.vp.Width))
	}
	m.vp.SetContent(sb.String())
	if m.follow {
		m.vp.GotoBottom()
	}
}

// minWrapWidth guards absurdly narrow viewports: below it, lines pass
// through rather than shredding into slivers.
const minWrapWidth = 8

// reflow word-wraps one logical line to width columns. Leading
// whitespace survives on the first fragment; long words hard-cut
// rune-safe; tabs expand to 4. ANSI-free input only: callers wrap raw
// text before styling.
func reflow(line string, width int) []string {
	if width < minWrapWidth {
		return []string{line}
	}
	line = strings.ReplaceAll(line, "\t", "    ")
	if line == "" {
		return []string{""}
	}
	var out []string
	for len([]rune(line)) > width {
		r := []rune(line)
		cut := width
		for i := width; i >= 0; i-- {
			if r[i] == ' ' {
				cut = i
				break
			}
			if i == 0 {
				cut = width
			}
		}
		out = append(out, strings.TrimRight(string(r[:cut]), " "))
		line = strings.TrimLeft(string(r[cut:]), " ")
	}
	out = append(out, line)
	return out
}

// allRoles lists every role for exhaustive tests (gutter width, etc.).
var allRoles = []role{
	roleAnswer, roleUser, roleThink, roleTool, roleResult,
	roleGate, roleMission, roleMarker, roleError, roleWarning,
}
