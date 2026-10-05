package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/keshon/tars/internal/agent"
	"github.com/keshon/tars/internal/llm"
	"github.com/keshon/tars/internal/session"
	"github.com/keshon/tars/internal/workspace"
)

// Sessions screen: a browsable list of past sessions with resume,
// rename, and delete. A session is a conversation: each task dir holds
// the whole turn history in state.json, so one row per dir.
//
// Storage follows pi, not an index: the title lives in the session dir
// (written once at creation, rewritten on rename), the screen scans on
// every open. No second source of truth means nothing to drift, no
// per-step write-path cost, and delete removes the title with the dir.
// Delete refuses the active session; rename allows it (the dir, and
// every -resume path, is unchanged).

type sessionsMode int

const (
	sessList sessionsMode = iota
	// sessRename borrows the reject-note input: single line, capped,
	// esc-backed. Enter commits, like the gate stager.
	sessRename
	// sessConfirm is a y/n delete confirmation over the selected row.
	sessConfirm
)

type sessionEntry struct {
	id      string
	title   string
	dir     string
	msgs    int
	updated time.Time
}

type sessionsState struct {
	entries []sessionEntry
	cursor  int
	// offset is the first visible row: the list scrolls under a
	// stationary viewport instead of clipping past it.
	offset int
	mode   sessionsMode
	flash  string
	// root is the tasks dir scanned at open; mutations re-scan it so
	// the list always reflects disk, never a stale copy.
	root string
}

// tasksRoot mirrors TaskDir's layout: relative, like every task path
// the CLI prints for -resume.
func tasksRoot() string {
	return filepath.Join(workspace.StateDirName, "tasks")
}

// listSessions scans session dirs newest-first. Titles prefer the
// stored title file, fall back to the session's first task text, then
// the id. Unreadable dirs still appear with a dash title: a corrupt
// snapshot is a session the user may want to delete.
func listSessions() []sessionEntry {
	return listSessionsIn(tasksRoot())
}

func listSessionsIn(root string) []sessionEntry {
	dirs, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []sessionEntry
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		dir := filepath.Join(root, d.Name())
		e := sessionEntry{id: d.Name(), dir: dir, title: workspace.ReadSessionTitle(dir)}
		state := filepath.Join(dir, "state.json")
		if fi, err := os.Stat(state); err == nil {
			e.updated = fi.ModTime()
			// Histories are context-bounded, but a pathological
			// snapshot must not stall an explicit user action.
			if fi.Size() < 64<<20 {
				if history, err := agent.LoadState(state); err == nil {
					e.msgs = len(history)
					if e.title == "" {
						e.title = session.TitleFor(history)
					}
				}
			}
		}
		if e.title == "" {
			e.title = e.id
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].updated.After(out[j].updated) })
	return out
}

// sessChrome counts non-entry rows in the box: title + hint, plus the
// flash line and rename input when shown. Each entry costs two rows
// (title + meta), so capacity is what remains halved.
func (m *model) sessChrome() int {
	chrome := 2
	if m.sessions.flash != "" {
		chrome++
	}
	if m.sessions.mode == sessRename {
		chrome++
	}
	return chrome
}

// sessVisible is how many entries fit the viewport. Stateless math
// shared by keys and render, so paging and clipping never disagree.
func (m *model) sessVisible() int {
	availH := m.termH - 3
	if m.ready {
		availH = m.vp.Height
	}
	maxRows := availH - 2
	if maxRows < 2 {
		maxRows = 2
	}
	if n := (maxRows - m.sessChrome()) / 2; n > 0 {
		return n
	}
	return 1
}

// clampOffset keeps the cursor on screen: follow down, pull up,
// pin to bounds after delete re-scans.
func (m *model) clampOffset() {
	s := m.sessions
	n := m.sessVisible()
	if s.offset > s.cursor {
		s.offset = s.cursor
	}
	if s.cursor >= s.offset+n {
		s.offset = s.cursor - n + 1
	}
	if max := len(s.entries) - n; max < 0 {
		s.offset = 0
	} else if s.offset > max {
		s.offset = max
	}
	if s.offset < 0 {
		s.offset = 0
	}
}

// ageString renders mtime as "5m ago"; zero time (no snapshot yet)
// renders a dash, never "0s ago".
func ageString(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// openSessions builds the list and pushes the overlay; closeSessions
// tears it down with focus restored. Balanced by construction: every
// open pairs with esc/enter or run start.
func (m *model) openSessions() {
	m.sessions = &sessionsState{entries: listSessions(), root: tasksRoot()}
	m.pushOverlay(ovSessions)
}

func (m *model) closeSessions() {
	m.sessions = nil
	m.resetNote()
	m.popOverlay()
}

// resetNote hands the borrowed input back with its own prompt and
// limit, or the next gate asks "name> ".
func (m *model) resetNote() {
	m.note.Blur()
	m.note.SetValue("")
	m.note.Prompt = "note> "
	m.note.CharLimit = 240
}

// sessionsKey handles keys while the screen is open. Letters are
// commands here (r/d/y), never typing — except rename mode, where the
// borrowed note input owns every key but esc/enter, like gsReject.
func (m *model) sessionsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := m.sessions
	if msg.String() == "ctrl+c" {
		m.quit = true
		m.cancel()
		return m, tea.Quit
	}
	if s.mode == sessRename {
		switch msg.Type {
		case tea.KeyEnter:
			m.commitRename()
			return m, nil
		case tea.KeyEsc:
			m.resetNote()
			s.mode = sessList
			return m, nil
		}
		var cmd tea.Cmd
		m.note, cmd = m.note.Update(msg)
		return m, cmd
	}
	if s.mode == sessConfirm {
		switch msg.String() {
		case "y", "Y":
			m.deleteSelected()
			return m, nil
		case "n", "N", "esc":
			s.mode = sessList
			s.flash = ""
			return m, nil
		}
		return m, nil
	}
	switch msg.String() {
	case "esc":
		m.closeSessions()
		return m, nil
	case "enter":
		return m, m.resumeSelected()
	case "up", "down":
		s.flash = ""
		if len(s.entries) == 0 {
			return m, nil
		}
		if msg.String() == "up" && s.cursor > 0 {
			s.cursor--
		}
		if msg.String() == "down" && s.cursor < len(s.entries)-1 {
			s.cursor++
		}
		m.clampOffset()
		return m, nil
	case "pgup", "pgdown":
		s.flash = ""
		if len(s.entries) == 0 {
			return m, nil
		}
		step := m.sessVisible()
		if msg.String() == "pgup" {
			s.cursor -= step
			if s.cursor < 0 {
				s.cursor = 0
			}
		} else {
			s.cursor += step
			if s.cursor > len(s.entries)-1 {
				s.cursor = len(s.entries) - 1
			}
		}
		m.clampOffset()
		return m, nil
	case "r", "R":
		if len(s.entries) == 0 {
			return m, nil
		}
		s.flash = ""
		s.mode = sessRename
		m.note.Prompt = "name> "
		m.note.CharLimit = 80
		m.note.SetValue("")
		return m, m.note.Focus()
	case "d", "D":
		if len(s.entries) == 0 {
			return m, nil
		}
		s.flash = ""
		s.mode = sessConfirm
		return m, nil
	}
	return m, nil
}

// resumeSelected switches to the selected session: snapshot loads,
// transcript rebuilds from history, follow-ups continue the
// conversation via the normal Resume path. Only at rest: switching
// mid-run would orphan the in-flight turn.
func (m *model) resumeSelected() tea.Cmd {
	s := m.sessions
	if len(s.entries) == 0 {
		return nil
	}
	if m.state != stDone {
		s.flash = "stop the run first"
		return nil
	}
	cur := s.entries[s.cursor]
	stateFile := filepath.Join(cur.dir, "state.json")
	history, err := agent.LoadState(stateFile)
	if err != nil {
		s.flash = "no snapshot to resume"
		return nil
	}
	task := session.TitleFor(history)
	if task == "" {
		task = cur.title
	}
	if m.newSession == nil {
		s.flash = "sessions unavailable here"
		return nil
	}
	sess, err := m.newSession(task, stateFile)
	if err != nil {
		s.flash = "cannot open session: " + err.Error()
		return nil
	}
	m.sess = sess
	m.stateFile = stateFile
	mb := markerBlock("— resumed " + cur.title + " —")
	mb.breakBefore = true
	m.blocks = append([]block{mb}, renderHistory(history)...)
	m.retryRun = nil
	m.runErr = nil
	m.answer = ""
	m.steps = 0
	m.tokens = 0
	m.state = stDone
	m.closeSessions()
	m.fitBottom()
	m.refreshContent()
	m.input.SetValue("")
	return m.input.Focus()
}

// renderHistory rebuilds the transcript from a loaded snapshot so a
// resumed or compacted session reads like it was never left. System
// messages and loop nudges ([harness] provenance) are history, not
// conversation, and stay out; tool cards re-attach their results by
// call ID, same pairing the live path uses. No markers: the caller
// prepends its own (resumed/compacted), this renders conversation.
func renderHistory(history []llm.Message) []block {
	var out []block
	for _, msg := range history {
		switch msg.Role {
		case llm.RoleSystem:
			continue
		case llm.RoleUser:
			if strings.HasPrefix(msg.Content, "[harness] ") {
				continue
			}
			if strings.TrimSpace(msg.Content) == "" {
				continue
			}
			out = append(out, userBlock(msg.Content))
		case llm.RoleAssistant:
			if msg.Reasoning != "" {
				out = append(out, thinkBlock(msg.Reasoning))
			}
			if strings.TrimSpace(msg.Content) != "" {
				out = append(out, answerBlock(msg.Content))
			}
			for _, tc := range msg.ToolCalls {
				out = append(out, toolCardBlock(tc.ID, tc.Name+" "+truncate(string(tc.Arguments), 120)))
			}
		case llm.RoleTool:
			attached := false
			if msg.ToolCallID != "" {
				for i := len(out) - 1; i >= 0; i-- {
					b := &out[i]
					if b.role == roleTool && b.open && b.callID == msg.ToolCallID {
						b.result = msg.Content
						b.open = false
						b.failed = strings.HasPrefix(msg.Content, "error:")
						attached = true
						break
					}
				}
			}
			if !attached {
				out = append(out, resultBlock(msg.Content))
			}
		}
	}
	return out
}

// commitRename stores the title beside the session (empty restores the
// derived text) and refreshes the row in place: order never moves
// under the cursor, only the title changes.
func (m *model) commitRename() {
	s := m.sessions
	if len(s.entries) == 0 {
		s.mode = sessList
		return
	}
	name := workspace.TitleLine(m.note.Value())
	m.resetNote()
	s.mode = sessList
	cur := &s.entries[s.cursor]
	if name == "" {
		// Clearing removes the stored title: the row falls back to
		// the derived text live, tracking history instead of a stale
		// copy of it.
		if history, err := agent.LoadState(filepath.Join(cur.dir, "state.json")); err == nil {
			name = session.TitleFor(history)
		}
		if name == "" {
			name = cur.id
		}
		_ = workspace.WriteSessionTitle(cur.dir, "")
	} else if err := workspace.WriteSessionTitle(cur.dir, name); err != nil {
		s.flash = "rename failed: " + err.Error()
		return
	}
	s.flash = ""
	cur.title = name
}

// deleteSelected removes the session dir — title goes with it, so no
// orphan rows — then re-scans. The active session refuses: its run
// still appends state there.
func (m *model) deleteSelected() {
	s := m.sessions
	if len(s.entries) == 0 {
		s.mode = sessList
		return
	}
	cur := s.entries[s.cursor]
	if m.stateFile != "" && cur.dir == filepath.Dir(m.stateFile) {
		s.mode = sessList
		s.flash = "cannot delete the active session"
		return
	}
	if err := os.RemoveAll(cur.dir); err != nil {
		s.mode = sessList
		s.flash = "delete failed: " + err.Error()
		return
	}
	s.entries = listSessionsIn(s.root)
	if s.cursor >= len(s.entries) && s.cursor > 0 {
		s.cursor--
	}
	m.clampOffset()
	s.mode = sessList
	s.flash = ""
}

// sessionsView renders the list centered like a dialog: cursor-marked
// rows, per-mode hint, rename input or delete question when staged.
// Widths clamp to the viewport; the viewport clips the rest.
func (m *model) sessionsView() string {
	s := m.sessions
	availW, availH := m.termW, m.termH-3
	if m.ready {
		availW, availH = m.vp.Width, m.vp.Height
	}
	inner := availW - 6
	if inner < 10 {
		inner = 10
	}
	rows := []string{m.styles.gate.Render("sessions")}
	if len(s.entries) == 0 {
		rows = append(rows, m.styles.dim.Render("(no sessions yet)"))
	}
	m.clampOffset()
	lo, hi := s.offset, s.offset+m.sessVisible()
	if hi > len(s.entries) {
		hi = len(s.entries)
	}
	for i, e := range s.entries[lo:hi] {
		mark := "  "
		if lo+i == s.cursor {
			mark = "> "
		}
		meta := e.id + " · " + ageString(e.updated)
		if e.msgs > 0 {
			meta += " · " + fmt.Sprintf("%d msgs", e.msgs)
		}
		rows = append(rows, truncate(mark+e.title, inner))
		rows = append(rows, m.styles.dim.Render("    "+truncate(meta, inner-4)))
	}
	switch s.mode {
	case sessRename:
		rows = append(rows, m.styles.dim.Render("new name (empty clears) · [enter] save · [esc] back"))
		rows = append(rows, truncate(m.note.View(), inner))
	case sessConfirm:
		rows = append(rows, m.styles.err.Render("delete \""+truncate(s.entries[s.cursor].title, 40)+"\"? [y]es / [n]o"))
	default:
		pos := ""
		if len(s.entries) > 0 {
			pos = fmt.Sprintf(" · %d/%d", s.cursor+1, len(s.entries))
		}
		rows = append(rows, m.styles.dim.Render("[enter] resume"+pos+" · [up/down] move · [r]ename · [d]elete · [esc] back"))
	}
	if s.flash != "" {
		rows = append(rows, m.styles.err.Render(truncate(s.flash, inner)))
	}
	maxRows := availH - 2
	if maxRows < 2 {
		maxRows = 2
	}
	if len(rows) > maxRows {
		rows = append(rows[:maxRows-1], rows[len(rows)-1])
	}
	return renderBox(rows, availW, availH)
}
