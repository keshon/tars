package tui

import (
	"fmt"
	"strings"
	"time"
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
)

// block is one transcript entry: raw content plus its role. Styling
// happens at render time, so toggles (think expand) re-render without
// touching history.
type block struct {
	role role
	text string
	// at stamps when the block entered the transcript. Rendered dim
	// as [15:04]; zero means "unstamped", rendered without a stamp.
	at time.Time
}

func answerBlock(s string) block  { return block{roleAnswer, s, time.Now()} }
func userBlock(s string) block    { return block{roleUser, s, time.Now()} }
func thinkBlock(s string) block   { return block{roleThink, s, time.Now()} }
func toolBlock(s string) block    { return block{roleTool, s, time.Now()} }
func resultBlock(s string) block  { return block{roleResult, s, time.Now()} }
func gateBlock(s string) block    { return block{roleGate, s, time.Now()} }
func missionBlock(s string) block { return block{roleMission, s, time.Now()} }
func markerBlock(s string) block  { return block{roleMarker, s, time.Now()} }
func errorBlock(s string) block   { return block{roleError, s, time.Now()} }

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
// line. Thinking renders collapsed unless expanded. Lines reflow to
// width columns (P10-6): the viewport never wraps, so unbroken long
// lines would vanish past the right edge. Continuation fragments take
// a blank gutter, marking them as wrapped rather than new.
func renderBlock(b block, st styles, expandThink bool, width int) string {
	raw := b.text
	if b.role == roleThink && !expandThink {
		raw = "⋯ " + thinkSummary(b.text) + " (" + thinkToggleHint + " to expand)"
	}
	stamped := !b.at.IsZero()
	logical := strings.Split(raw, "\n")
	var out []string
	for li, line := range logical {
		w := width - gutterWidth
		if stamped && li == len(logical)-1 {
			w -= stampWidth
		}
		frags := reflow(line, w)
		for i, f := range frags {
			g := st.gutter(b.role)
			if i > 0 {
				g = "  "
			}
			out = append(out, g+applyRoleStyle(b.role, line, f, st))
		}
	}
	if stamped {
		out[len(out)-1] += st.dim.Render(" [" + b.at.Format("15:04") + "]")
	}
	return strings.Join(out, "\n")
}

// stampWidth reserves room for the " [15:04]" suffix when wrapping the
// block's last line, so the stamp never pushes it past the edge.
const stampWidth = 8

// applyRoleStyle colors one (already wrapped) fragment. Result blocks
// keep the original line's diff prefix so continuations hold their color.
func applyRoleStyle(r role, origLine, frag string, st styles) string {
	switch r {
	case roleAnswer:
		return frag
	case roleUser:
		return st.user.Render(frag)
	case roleThink:
		return st.think.Render(frag)
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

// refreshContent re-renders every block into the viewport. Called on
// append, on toggles, and on resize (reflow follows the width). YOffset
// survives SetContent, so a reading user is never yanked (pinned by
// TestNewBlocksRespectUnfollow).
func (m *model) refreshContent() {
	if !m.ready {
		return
	}
	var sb strings.Builder
	for i, b := range m.blocks {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(renderBlock(b, m.styles, m.showThink, m.vp.Width))
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
	roleGate, roleMission, roleMarker, roleError,
}
