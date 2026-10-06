package tui

import (
	"path/filepath"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
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
	m.input.Blur()
	m.fitBottom()
}

func (m *model) closeDialog() {
	m.dialog = nil
	m.popOverlay()
	m.fitBottom()
}

// dialogView renders the open dialog centered in the viewport area:
// title, body, close hint. Widths are clamped to the viewport; heights
// are top-padded to center, the viewport clips the rest.
func (m *model) dialogView() string {
	d := m.dialog
	availW, availH := m.termW, m.termH-3
	if m.ready {
		availW, availH = m.vp.Width(), m.vp.Height()
	}
	inner := availW - 6
	if inner < 10 {
		inner = 10
	}
	rows := []string{m.styles.gate.Render(truncate(d.title, inner))}
	for _, ln := range d.lines {
		rows = append(rows, truncate(ln, inner))
	}
	rows = append(rows, m.styles.dim.Render("[Esc] close"))
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
	border := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	framed := []string{border.Render("╭" + strings.Repeat("─", bw+2) + "╮")}
	for _, row := range rows {
		framed = append(framed, border.Render("│ ")+cellLine(row, bw)+border.Render(" │"))
	}
	framed = append(framed, border.Render("╰"+strings.Repeat("─", bw+2)+"╯"))
	box := strings.Join(framed, "\n")
	top := (availH - lipgloss.Height(box)) / 2
	if top < 0 {
		top = 0
	}
	return strings.Repeat("\n", top) + lipgloss.PlaceHorizontal(availW, lipgloss.Center, box)
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
			{"F1 / F2 / F3", "help / sessions / details"},
			{"F4 / F5 / F6 / F7", "new / mode / sidebar / latest"},
			{"F10", "quit"},
			{"Ctrl+Q", "quit"},
			{"Esc", "stop run, stay in chat"},
			{"Enter", "queue a follow-up during a run"},
			{"Ctrl+C", "abort (quits mid-run)"},
			{"Y / N / A", "answer permission"},
			{"Enter", "submits"},
			{"Shift+Enter / Ctrl+O", "newline"},
			{"Ctrl+P", "search saved sessions"},
			{"Ctrl+B", "show/hide sidebar"},
			{"Tab", "switch sidebar/input"},
			{"Ctrl+N", "new chat when idle"},
			{"Alt+Up / Alt+Down", "history"},
			{"Ctrl+G", "expand/collapse thinking and tool details"},
			{"PgUp / PgDn + wheel", "scroll"},
			{"Ctrl+End", "jump to latest in every state"},
			{"Ctrl+U", "clear draft deliberately"},
			{"Ctrl+E / Ctrl+X", "edit / cancel queued follow-up"},
		}},
		{"gates", [][2]string{
			{"Y", "once"},
			{"A", "always for this run (confirm)"},
			{"N", "reject (a note redirects the model)"},
		}},
		{"commands", [][2]string{
			{"/quit", "exit"},
			{"/help", "this list"},
			{"/new [task]", "fresh task (empty resets to chat)"},
			{"/status", "run facts"},
			{"/mode plan|act", "preview changes or execute"},
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
	// The toggle's own name comes from the bottom bar ("Ctrl+G
	// details"), not the history internals: compact here describes the
	// transcript view, never context compaction.
	details := "collapsed (Ctrl+G)"
	if !m.compact {
		details = "full (Ctrl+G)"
	}
	// Zero means the loop's default; the TUI must not print "0".
	budget := "default"
	if m.thinkBudget > 0 {
		budget = strconv.Itoa(m.thinkBudget) + " chars"
	}
	// Run-local scope matters: these grants die with the process.
	allowed := "none"
	m.alwaysMu.Lock()
	n := len(m.always)
	m.alwaysMu.Unlock()
	if n > 0 {
		allowed = strconv.Itoa(n) + " (this run only)"
	}
	return kvRows([]string{
		"backend: " + nonEmpty(m.backendKind),
		"model: " + nonEmpty(m.modelName),
		"workspace: " + nonEmpty(m.wsRoot),
		"task dir: " + taskDir,
		"context: " + m.meter(),
		"run: " + strconv.Itoa(m.steps) + " steps  " + m.statusWord() + "  " + formatElapsed(m.elapsed),
		"details: " + details,
		"thinking budget: " + budget,
		"permissions: Y allow once  A always allow  N deny with note",
		"always allowed: " + allowed,
		"tools: " + strconv.Itoa(m.mcpCount) + " mcp  " + strconv.Itoa(m.policyRules) + " policy rules",
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
