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
	title        string
	lines        []string
	help         bool
	page, offset int
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
	state := *m.dialog
	d := &state
	availW, availH := m.termW, m.termH-3
	if m.ready {
		availW, availH = m.vp.Width(), m.vp.Height()
	}
	inner := min(availW-6, 72)
	if inner < 10 {
		inner = 10
	}
	rows := []string{m.styles.gate.Render(truncate(d.title, inner))}
	lines := d.lines
	hint := "Esc / Enter close"
	if d.help {
		sections := helpSections()
		tabs := make([]string, len(sections))
		for i, section := range sections {
			label := section.title
			if i == d.page {
				label = m.styles.gate.Render("[" + label + "]")
			} else {
				label = m.styles.dim.Render(label)
			}
			tabs[i] = label
		}
		rows = append(rows, strings.Split(lipgloss.NewStyle().Width(inner).Render(strings.Join(tabs, "  ")), "\n")...)
		if availH >= 10 {
			rows = append(rows, "")
		}
		lines = m.helpRows(sections[d.page], inner)
		hint = "Tab / ← → category   ↑ ↓ scroll   Esc close"
	}
	capacity := max(availH-2-len(rows)-3, 1)
	d.offset = min(max(d.offset, 0), max(len(lines)-capacity, 0))
	for _, ln := range lines[d.offset:min(d.offset+capacity, len(lines))] {
		rows = append(rows, cellLine(ln, inner))
	}
	rows = append(rows, "")
	if len(lines) > capacity {
		rows = append(rows, m.styles.dim.Render(strconv.Itoa(d.offset+1)+"–"+strconv.Itoa(min(d.offset+capacity, len(lines)))+" / "+strconv.Itoa(len(lines))))
	}
	rows = append(rows, m.styles.dim.Render(truncate(hint, inner)))
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
	border := lipgloss.NewStyle().Foreground(lipgloss.Color(colorDim))
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

func (m *model) openHelp() {
	m.openDialog("Help", nil)
	m.dialog.help = true
}

func helpSections() []helpSection {
	return []helpSection{
		{"Input", [][2]string{
			{"Enter", "Send a message; queue a follow-up while running"},
			{"Shift+Enter / Ctrl+O", "Insert a new line"},
			{"Up / Down", "Move within the draft"},
			{"Alt+Up / Alt+Down", "Recall input history"},
			{"Ctrl+U", "Clear the draft"},
			{"/ or @", "Suggest commands or workspace paths"},
			{"Enter / Tab / Esc", "Run a command, insert a suggestion, or dismiss the picker"},
			{"Ctrl+E", "Move the queued follow-up back to the draft"},
			{"Ctrl+X", "Cancel the queued follow-up"},
			{"@\"image path\"", "Attach a workspace file or image; quote paths with spaces"},
		}},
		{"Navigate", [][2]string{
			{"F1", "Open or close help"},
			{"F2 / Ctrl+P", "Search session names and conversation history"},
			{"F3 / Ctrl+G", "Expand or collapse thinking and tool details"},
			{"F6 / Ctrl+B", "Show or hide the session pane"},
			{"Tab", "Switch between sessions and input"},
			{"Ctrl+R / Ctrl+D", "Rename / delete the selected session in the Sessions pane"},
			{"PgUp / PgDn", "Scroll the conversation; mouse wheel also works"},
			{"F7 / Ctrl+End", "Jump to the latest message"},
			{"F9", "Focus header badges; Left / Right choose, Enter opens"},
			{"Header menus", "Click a badge; Up / Down browse, Enter choose, Esc back"},
		}},
		{"Run", [][2]string{
			{"F4 / Ctrl+N", "Start a new session when idle"},
			{"F5", "Switch Plan / Act mode when idle"},
			{"Esc", "Stop the current run; keep the session open"},
			{"Y", "Allow the requested tool once"},
			{"A", "Allow for this run, after confirmation"},
			{"N", "Reject a tool; optionally add a redirect note"},
			{"Ctrl+Q / F10", "Quit when idle; stop an active run first"},
			{"Ctrl+C", "Abort and quit, including during a run"},
		}},
		{"Commands", commandReference()},
	}
}

// Keep descriptions complete: narrow terminals stack them below the keys.
func (m *model) helpRows(section helpSection, width int) []string {
	keyWidth := 0
	for _, row := range section.rows {
		keyWidth = max(keyWidth, lipgloss.Width(row[0]))
	}
	var rows []string
	for _, row := range section.rows {
		if width-keyWidth-3 < 24 {
			rows = append(rows, m.styles.gate.Render(row[0]))
			for _, line := range strings.Split(lipgloss.NewStyle().Width(max(width-2, 1)).Render(row[1]), "\n") {
				rows = append(rows, "  "+line)
			}
		} else {
			description := strings.Split(lipgloss.NewStyle().Width(width-keyWidth-3).Render(row[1]), "\n")
			rows = append(rows, m.styles.gate.Render(cellLine(row[0], keyWidth))+"   "+description[0])
			for _, line := range description[1:] {
				rows = append(rows, strings.Repeat(" ", keyWidth+3)+line)
			}
		}
		rows = append(rows, "")
	}
	return rows[:max(len(rows)-1, 0)]
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
