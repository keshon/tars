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
// line. Thinking renders collapsed unless expanded.
func renderBlock(b block, st styles, expandThink bool) string {
	var body string
	switch b.role {
	case roleAnswer:
		body = b.text
	case roleUser:
		body = st.user.Render(b.text)
	case roleThink:
		if expandThink {
			body = st.think.Render(b.text)
		} else {
			body = st.think.Render("⋯ " + thinkSummary(b.text) + " (" + thinkToggleHint + " to expand)")
		}
	case roleTool:
		body = st.dim.Render(b.text)
	case roleResult:
		body = renderResult(b.text, st)
	case roleGate:
		body = st.gate.Render(b.text)
	case roleMission:
		body = st.dim.Render(b.text)
	case roleMarker:
		body = st.dim.Render(b.text)
	case roleError:
		body = st.err.Render(b.text)
	}
	if !b.at.IsZero() {
		body += st.dim.Render(" [" + b.at.Format("15:04") + "]")
	}
	g := st.gutter(b.role)
	lines := strings.Split(body, "\n")
	for i := range lines {
		lines[i] = g + lines[i]
	}
	return strings.Join(lines, "\n")
}

// appendBlock records a block and re-renders the transcript.
func (m *model) appendBlock(b block) {
	m.blocks = append(m.blocks, b)
	m.refreshContent()
}

// refreshContent re-renders every block into the viewport. Called on
// append and on toggles; YOffset survives SetContent, so a reading
// user is never yanked (pinned by TestNewBlocksRespectUnfollow).
func (m *model) refreshContent() {
	if !m.ready {
		return
	}
	var sb strings.Builder
	for i, b := range m.blocks {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(renderBlock(b, m.styles, m.showThink))
	}
	m.vp.SetContent(sb.String())
	if m.follow {
		m.vp.GotoBottom()
	}
}

// renderResult colors unified-diff lines; other text stays dim.
func renderResult(text string, st styles) string {
	var b strings.Builder
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "++"):
			b.WriteString(st.add.Render(line))
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "--"):
			b.WriteString(st.del.Render(line))
		case strings.HasPrefix(line, "@@"):
			b.WriteString(st.hunk.Render(line))
		default:
			b.WriteString(st.dim.Render(line))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// allRoles lists every role for exhaustive tests (gutter width, etc.).
var allRoles = []role{
	roleAnswer, roleUser, roleThink, roleTool, roleResult,
	roleGate, roleMission, roleMarker, roleError,
}
