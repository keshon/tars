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

// helpLines documents keys and commands in one place: the /help dialog
// and nothing else, so help can never rot in two copies.
func helpLines() []string {
	return []string{
		"keys: ctrl+q quit · q/ctrl+c abort run · esc interrupt, stay in chat · y/n/a answer permission",
		"      enter submits · ctrl+o newline · up/down history · ctrl+g thinking",
		"      pgup/pgdn + wheel scroll · end back to live",
		"gates: [y]es once / [a]lways for this run (confirm) / [n]o (a note redirects the model)",
		"commands: /quit /help /new <task> /status /retry",
	}
}

// statusLines snapshots run facts for the /status dialog. Everything
// shown is model state or construction facts; the TUI never re-reads
// Env, so nothing here can disagree with the run.
func (m *model) statusLines() []string {
	taskDir := "—"
	if m.stateFile != "" {
		taskDir = filepath.Dir(m.stateFile)
	}
	think := "collapsed"
	if m.showThink {
		think = "expanded"
	}
	// Zero means the loop's default; the TUI must not print "0".
	budget := strconv.Itoa(m.thinkBudget)
	if m.thinkBudget <= 0 {
		budget = "default"
	}
	return []string{
		"backend: " + nonEmpty(m.backendKind),
		"workspace: " + nonEmpty(m.wsRoot),
		"task dir: " + taskDir,
		"context: " + m.meter(),
		"run: steps " + strconv.Itoa(m.steps) + " · " + m.statusWord() + " · " + formatElapsed(m.elapsed),
		"thinking: budget " + budget + " chars · " + think,
		"gates: staged y/a/n · always-memory: " + strconv.Itoa(len(m.always)) + " pairs",
		"tools: mcp " + strconv.Itoa(m.mcpCount) + " · policy rules " + strconv.Itoa(m.policyRules),
	}
}

func nonEmpty(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
