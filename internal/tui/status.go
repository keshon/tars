package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// statusLine renders the exactly-one-line status: steps, context
// meter, elapsed + spinner while running, state, follow hint.
func (m *model) statusLine() string {
	follow := ""
	if !m.follow {
		follow = " · ↓ end"
	}
	return m.styles.status.Render(fmt.Sprintf(
		"steps %d · %s · elapsed %s · %s%s%s",
		m.steps, m.meter(), formatElapsed(m.elapsed), m.spinner(), m.statusWord(), follow))
}

func (m *model) statusWord() string {
	switch m.state {
	case stPermission:
		return "awaiting permission"
	case stAsk:
		return "awaiting answer"
	case stDone:
		return "done"
	default:
		return "running"
	}
}

// formatElapsed renders a duration as clock time: MM:SS, H:MM:SS past
// the hour. Elapsed is per-run wall clock (reset in startRun, frozen
// on done), never idle reading time.
func formatElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int(d.Round(time.Second).Seconds())
	h, m, sec := s/3600, (s%3600)/60, s%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, sec)
	}
	return fmt.Sprintf("%02d:%02d", m, sec)
}

// spinnerFrames pulses the TARS name on the existing 1s tick —
// memorable, ASCII-only (zero font risk), no extra messages or
// goroutines: the frame index falls out of elapsed.
var spinnerFrames = []string{"Tars", "tArs", "taRs", "tarS"}

// spinner shows the current frame while a run is in flight, else
// nothing: paired with elapsed, time-since-submit stays visible while
// the backend is silent.
func (m *model) spinner() string {
	if m.state != stRunning {
		return ""
	}
	return spinnerFrames[int(m.elapsed.Seconds())%len(spinnerFrames)] + " "
}

// meter renders the context fill from the latest backend-reported prompt
// size: "12.4k / 131k (9%)". Unknown window or no measurement yet shows
// a dash instead of a guess — the loop itself treats zero limits as
// "no budget tracking", and the display follows the same rule.
func (m *model) meter() string {
	if m.limit <= 0 || m.tokens <= 0 {
		return "ctx —"
	}
	pct := m.tokens * 100 / m.limit
	return fmt.Sprintf("ctx %s / %s (%d%%)", kTokens(m.tokens), kTokens(m.limit), pct)
}

func kTokens(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

// threadWidth caps the role strip to the most recent blocks.
const threadWidth = 12

// dividerLine splits history from the bottom zone: a dim rule carrying
// the thread strip (last blocks by role) right-aligned on it. One
// line, two jobs; the status line below stays a status line.
func (m *model) dividerLine() string {
	strip := " " + m.threadStrip()
	w := m.termW
	if m.ready && m.vp.Width > 0 {
		w = m.vp.Width
	}
	rule := w - lipgloss.Width(strip)
	if rule < minWrapWidth {
		rule = minWrapWidth
	}
	return m.styles.dim.Render(strings.Repeat("─", rule)) + strip
}

// threadStrip compresses recent transcript makeup into one glance:
// role glyphs for the last blocks, most-recent rightmost. It answers
// "what is my context made of" honestly — block roles, never invented
// token shares (the backend reports totals only).
func (m *model) threadStrip() string {
	start := 0
	if len(m.blocks) > threadWidth {
		start = len(m.blocks) - threadWidth
	}
	var sb strings.Builder
	sb.WriteString(m.styles.dim.Render("thread "))
	for _, b := range m.blocks[start:] {
		sb.WriteString(threadGlyph(b.role, m.styles))
	}
	return sb.String()
}

// threadGlyph maps a role to one width-1 cell in its role color.
// Shapes differ, not just colors, so the strip survives monochrome.
func threadGlyph(r role, st styles) string {
	switch r {
	case roleUser:
		return st.user.Render("●")
	case roleAnswer:
		return "○"
	case roleThink:
		return st.think.Render("~")
	case roleTool:
		return st.dim.Render("→")
	case roleResult:
		return st.dim.Render("=")
	case roleGate:
		return st.gate.Render("?")
	case roleMarker:
		return st.dim.Render("-")
	case roleError:
		return st.err.Render("!")
	case roleWarning:
		return st.warn.Render("»")
	default:
		return " "
	}
}
