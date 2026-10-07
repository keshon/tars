package tui

import (
	"fmt"
	"strings"
	"time"
)

// statusLine describes the current input; run metrics belong in the header.
func (m *model) statusLine() string {
	if m.sessions != nil {
		return m.styles.status.Render("Search names and saved conversation text")
	}
	if m.dialog != nil {
		return m.styles.status.Render("Esc close  F1 close help")
	}
	hint := m.actionHint()
	if !m.follow && m.state == stDone {
		hint += "  Scrolled up"
	}
	return m.styles.status.Render(hint)
}

func (m *model) runFacts() string {
	if m.sessions != nil || m.dialog != nil || (m.state == stDone && m.steps == 0 && m.tokens == 0 && m.toolsUsed == 0 && m.elapsed == 0) {
		return ""
	}
	facts := fmt.Sprintf("step %d/%d  %s  elapsed %s%s", m.steps, m.maxSteps, m.meter(), formatElapsed(m.elapsed), m.ttft())
	if m.toolsUsed > 0 {
		facts += fmt.Sprintf("  tools %d", m.toolsUsed)
	}
	if len(m.filesTouched) > 0 {
		facts += fmt.Sprintf("  files %d", len(m.filesTouched))
	}
	return facts
}

// ttft renders time-to-first-token once streaming starts: the latency
// felt, beside elapsed (the total). Empty before the first chunk and
// outside runs, so the line never promises what it can't show.
func (m *model) ttft() string {
	if m.state != stRunning || m.firstToken.IsZero() {
		return ""
	}
	return "  ttft " + formatElapsed(m.firstToken.Sub(m.started))
}

func (m *model) statusWord() string {
	switch m.state {
	case stPermission:
		return "awaiting permission"
	case stAsk:
		return "waiting"
	case stStopping:
		return "stopping"
	case stDone:
		if m.runErr != nil {
			return "failed"
		}
		return "ready"
	default:
		return "working"
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

// The application name stays static; activity belongs beside the run status.
func (m *model) brand() string { return m.styles.gate.Render("TARS") + " " }

var activityFrames = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

func (m *model) busy() bool { return m.state == stRunning || m.state == stStopping }

func (m *model) spinner() string {
	if !m.busy() {
		return ""
	}
	return m.styles.gate.Render(string(activityFrames[m.spinnerFrame%len(activityFrames)]))
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
	w := max(m.termW, 0)
	if w < minWrapWidth {
		w = minWrapWidth
	}
	return m.styles.dim.Render(strings.Repeat("─", w))
}
