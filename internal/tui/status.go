package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// statusLine renders the exactly-one-line status: run facts left,
// brand + state right ("TARS working…", action in gray). Keys live
// only in /help now: caret notation (^g) taught nothing, and a manual
// in the status row wastes it every second of every run.
func (m *model) statusLine() string {
	follow := ""
	if !m.follow {
		follow = " · ↓ end"
	}
	left := fmt.Sprintf(
		"step %d/%d · %s · elapsed %s%s%s",
		m.steps, m.maxSteps, m.meter(), formatElapsed(m.elapsed), m.ttft(), follow)
	right := m.brand() + padAction(m.statusWord())
	if right == "" {
		return m.styles.status.Render(left)
	}
	w := m.termW
	if m.ready && m.vp.Width > 0 {
		w = m.vp.Width
	}
	if pad := w - lipgloss.Width(left) - lipgloss.Width(right); pad >= 2 {
		return m.styles.status.Render(left + strings.Repeat(" ", pad) + right)
	}
	return m.styles.status.Render(left)
}

// ttft renders time-to-first-token once streaming starts: the latency
// felt, beside elapsed (the total). Empty before the first chunk and
// outside runs, so the line never promises what it can't show.
func (m *model) ttft() string {
	if m.state != stRunning || m.firstToken.IsZero() {
		return ""
	}
	return " · ttft " + formatElapsed(m.firstToken.Sub(m.started))
}

func (m *model) statusWord() string {
	switch m.state {
	case stPermission:
		return "awaiting permission"
	case stAsk:
		return "waiting"
	case stDone:
		return "ready"
	default:
		return "thinking"
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

// actionWidth fixes the state word's column width so the brand never
// shifts when running flips to done ("thinking" fills it exactly).
// Gate words run longer and overflow left instead — modal states own
// the screen anyway, so their shift is expected, not jitter.
const actionWidth = 8

// padAction right-pads short state words with spaces (rune-aware:
// wide runes must not split). Trailing spaces are invisible but keep
// the brand column — and the right edge — perfectly still.
func padAction(word string) string {
	if n := len([]rune(word)); n < actionWidth {
		return word + strings.Repeat(" ", actionWidth-n)
	}
	return word
}

// brand renders TARS: one amber letter bouncing back and forth while
// running, the whole word amber at rest. The action word stays gray —
// highlight belongs to the brand alone.
func (m *model) brand() string {
	if m.state == stRunning {
		return m.spinner()
	}
	return m.styles.gate.Render("TARS") + " "
}

// spinner animates the brand while a run is in flight, else nothing:
// paired with elapsed, time-since-submit stays visible while the
// backend is silent. ASCII-only (zero font risk), no extra messages or
// goroutines: the frame index falls out of elapsed.
func (m *model) spinner() string {
	if m.state != stRunning {
		return ""
	}
	active := pingPong(int(m.elapsed.Seconds()))
	var b strings.Builder
	for i, r := range "TARS" {
		if i == active {
			b.WriteString(m.styles.gate.Render(string(r)))
		} else {
			b.WriteString(string(r))
		}
	}
	return b.String() + " "
}

// pingPong bounces 0-1-2-3-2-1 over a 6s period: the bounce reads
// calmer than a hard jump back to T.
func pingPong(s int) int {
	frames := []int{0, 1, 2, 3, 2, 1}
	return frames[s%len(frames)]
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
	s := fmt.Sprintf("ctx %s / %s (%d%%)", kTokens(m.tokens), kTokens(m.limit), pct)
	if m.tokensEst {
		return "~" + s
	}
	return s
}

func kTokens(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

// dividerLine splits history from the bottom zone with a dim rule.
func (m *model) dividerLine() string {
	w := m.termW
	if m.ready && m.vp.Width > 0 {
		w = m.vp.Width
	}
	if w < minWrapWidth {
		w = minWrapWidth
	}
	return m.styles.dim.Render(strings.Repeat("─", w))
}
