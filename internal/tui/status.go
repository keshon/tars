package tui

import (
	"fmt"
	"strings"
	"time"
)

// statusLine renders the exactly-one-line status: steps, context
// meter, elapsed + spinner while running, state, follow hint.
func (m *model) statusLine() string {
	follow := ""
	if !m.follow {
		follow = " · ↓ end"
	}
	return m.styles.status.Render(fmt.Sprintf(
		"step %d/%d · %s · elapsed %s · %s%s%s",
		m.steps, m.maxSteps, m.meter(), formatElapsed(m.elapsed), m.spinner(), m.statusWord(), follow))
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

// spinner shows TARS while a run is in flight, else nothing: paired
// with elapsed, time-since-submit stays visible while the backend is
// silent. The "active" letter cycles in amber on the existing 1s tick —
// memorable, ASCII-only (zero font risk), no extra messages or
// goroutines: the frame index falls out of elapsed.
func (m *model) spinner() string {
	if m.state != stRunning {
		return ""
	}
	active := int(m.elapsed.Seconds()) % len("TARS")
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
