package tui

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/keshon/tars/internal/workspace"
)

// wsRootOf names the workspace root, empty when none (unit models
// render a dash via nonEmpty).
func wsRootOf(ws *workspace.Workspace) string {
	if ws == nil {
		return ""
	}
	return ws.Root()
}

// dialog is a centered modal over the transcript: status, help, and
// future pickers. Takeover, not compositing: while open it replaces
// the transcript body, so no ANSI splicing is needed and scroll state
// underneath survives untouched.
type dialog struct {
	title string
	lines []string
}

// openDialog pushes the overlay and shows the dialog; closeDialog pops
// it. Balanced by construction: every open path pairs with esc/enter
// or run start.
func (m *model) openDialog(title string, lines []string) {
	m.dialog = &dialog{title: title, lines: lines}
	m.pushOverlay(ovDialog)
}

func (m *model) closeDialog() {
	m.dialog = nil
	m.popOverlay()
}

// dialogView renders the open dialog centered in the viewport area:
// title, body, close hint. Widths are clamped to the viewport; heights
// are top-padded to center, the viewport clips the rest.
func (m *model) dialogView() string {
	d := m.dialog
	availW, availH := m.termW, m.termH-3
	if m.ready {
		availW, availH = m.vp.Width, m.vp.Height
	}
	inner := availW - 6
	if inner < 10 {
		inner = 10
	}
	rows := []string{m.styles.gate.Render(truncate(d.title, inner))}
	for _, ln := range d.lines {
		rows = append(rows, truncate(ln, inner))
	}
	rows = append(rows, m.styles.dim.Render("[esc] close"))
	// Cap rows to the viewport: title + hint always survive the cut.
	maxRows := availH - 2
	if maxRows < 2 {
		maxRows = 2
	}
	if len(rows) > maxRows {
		rows = append(rows[:maxRows-1], rows[len(rows)-1])
	}
	return renderBox(rows, availW, availH)
}

// renderBox centers rows in a bordered box: shared by the dialog and
// the sessions screen, so overlay chrome can never drift in two.
func renderBox(rows []string, availW, availH int) string {
	bw := 0
	for _, r := range rows {
		if n := lipgloss.Width(r); n > bw {
			bw = n
		}
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("8")).
		Padding(0, 1).
		// Width covers padding: content gets bw back, so nothing
		// re-wraps inside the box.
		Width(bw + 2).
		Render(strings.Join(rows, "\n"))
	top := (availH - lipgloss.Height(box)) / 2
	if top < 0 {
		top = 0
	}
	return strings.Repeat("\n", top) + box
}

// helpSection groups rows under a title for the two-column help:
// keys left, descriptions right, aligned per section. Single copy
// shared by the command and tests, so help can never rot in two.
type helpSection struct {
	title string
	rows  [][2]string
}

func helpSections() []helpSection {
	return []helpSection{
		{"keys", [][2]string{
			{"ctrl+q", "quit"},
			{"q / esc", "stop run, stay in chat"},
			{"ctrl+c", "abort (quits mid-run)"},
			{"y / n / a", "answer permission"},
			{"enter", "submits"},
			{"ctrl+o", "newline"},
			{"up / down", "history"},
			{"ctrl+g", "compact history"},
			{"pgup / pgdn + wheel", "scroll"},
			{"end", "back to live"},
		}},
		{"gates", [][2]string{
			{"y", "once"},
			{"a", "always for this run (confirm)"},
			{"n", "reject (a note redirects the model)"},
		}},
		{"commands", [][2]string{
			{"/quit", "exit"},
			{"/help", "this list"},
			{"/new [task]", "fresh task (empty resets to chat)"},
			{"/status", "run facts"},
			{"/retry", "re-run last failed turn"},
			{"/sessions", "past sessions"},
			{"/compact", "shrink this session's history"},
		}},
	}
}

// renderHelp lays sections out: bold title, key column padded to the
// section max, descriptions after. Single copy shared by the command
// and tests, so help can never rot in two copies.
func renderHelp() []string {
	var out []string
	for _, s := range helpSections() {
		out = append(out, "# "+s.title)
		w := 0
		for _, r := range s.rows {
			if n := len([]rune(r[0])); n > w {
				w = n
			}
		}
		for _, r := range s.rows {
			out = append(out, "  "+r[0]+strings.Repeat(" ", w-len([]rune(r[0]))+2)+r[1])
		}
	}
	return out
}

// statusLines snapshots run facts for the /status dialog. Everything
// shown is model state or construction facts; the TUI never re-reads
// Env, so nothing here can disagree with the run.
func (m *model) statusLines() []string {
	taskDir := "—"
	if m.stateFile != "" {
		taskDir = filepath.Dir(m.stateFile)
	}
	// The toggle's own name comes from the bottom bar ("ctrl+g
	// details"), not the history internals: compact here describes the
	// transcript view, never context compaction.
	details := "collapsed (ctrl+g)"
	if !m.compact {
		details = "full (ctrl+g)"
	}
	// Zero means the loop's default; the TUI must not print "0".
	budget := "default"
	if m.thinkBudget > 0 {
		budget = strconv.Itoa(m.thinkBudget) + " chars"
	}
	// Run-local scope matters: these grants die with the process.
	allowed := "none"
	if n := len(m.always); n > 0 {
		allowed = strconv.Itoa(n) + " (this run only)"
	}
	return kvRows([]string{
		"backend: " + nonEmpty(m.backendKind),
		"model: " + nonEmpty(m.modelName),
		"workspace: " + nonEmpty(m.wsRoot),
		"task dir: " + taskDir,
		"context: " + m.meter(),
		"run: " + strconv.Itoa(m.steps) + " steps · " + m.statusWord() + " · " + formatElapsed(m.elapsed),
		"details: " + details,
		"thinking budget: " + budget,
		"permissions: y allow once · a always allow · n deny with note",
		"always allowed: " + allowed,
		"tools: " + strconv.Itoa(m.mcpCount) + " mcp · " + strconv.Itoa(m.policyRules) + " policy rules",
	})
}

func nonEmpty(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// kvRows aligns "label: value" rows so values start in one column:
// short labels are padded, never truncated. Borderless on purpose —
// status facts, not data grids (markdown tables already cover those).
func kvRows(rows []string) []string {
	w := 0
	for _, r := range rows {
		if i := strings.Index(r, ":"); i > w {
			w = i
		}
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		if j := strings.Index(r, ":"); j >= 0 {
			out[i] = r[:j] + strings.Repeat(" ", w-j) + r[j:]
		} else {
			out[i] = r
		}
	}
	return out
}
