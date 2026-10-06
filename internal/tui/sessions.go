package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/keshon/tars/internal/agent"
	"github.com/keshon/tars/internal/llm"
	"github.com/keshon/tars/internal/session"
	"github.com/keshon/tars/internal/workspace"
)

// Sessions screen: a browsable list of past sessions with open,
// rename, and delete. A session is a conversation: each task dir holds
// the whole turn history in state.json, so one row per dir.
//
// Storage follows pi, not an index: the title lives in the session dir
// (written once at creation, rewritten on rename), the screen scans on
// every open. No second source of truth means nothing to drift, no
// per-step write-path cost, and delete removes the title with the dir.
// Delete unloads the active session at rest; rename allows it (the dir, and
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
	status  string
	mode    string
	preview string
}

type sessionsState struct {
	entries []sessionEntry
	all     []sessionEntry
	filter  textinput.Model
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
		e := sessionEntry{id: d.Name(), dir: dir, title: workspace.ReadSessionTitle(dir), status: "Saved", mode: readChatMode(dir)}
		state := filepath.Join(dir, "state.json")
		if fi, err := os.Stat(state); err == nil {
			e.status = "Unreadable"
			e.updated = fi.ModTime()
			// Histories are context-bounded, but a pathological
			// snapshot must not stall an explicit user action.
			if fi.Size() < 64<<20 {
				if history, err := agent.LoadState(state); err == nil {
					e.status = "Saved"
					e.msgs = len(history)
					if _, question, paused := agent.PausedOnQuestion(history); paused {
						e.status, e.preview = "Needs input", question
					} else {
						for i := len(history) - 1; i >= 0; i-- {
							if history[i].Role == llm.RoleAssistant && history[i].Content != "" {
								e.preview = truncate(history[i].Content, 500)
								break
							}
						}
					}
					if e.title == "" {
						e.title = session.TitleFor(history)
					}
				}
			}
		}
		if fi, err := os.Stat(filepath.Join(dir, "mission.json")); err == nil {
			e.mode = "Mission"
			if e.updated.IsZero() {
				e.updated = fi.ModTime()
			}
			e.preview = "Mission snapshot. Resume with agent -tui -resume " + dir
		}
		if e.updated.IsZero() {
			continue
		}
		if e.title == "" {
			e.title = e.id
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].updated.After(out[j].updated) })
	return out
}

// sessChrome counts title, search, gap, and hint, plus the
// flash line and rename input when shown. Each entry costs three rows
// (title + meta + gap), so capacity is what remains divided by three.
func (m *model) sessChrome() int {
	chrome := 4
	if m.sessions.flash != "" {
		chrome++
	}
	if m.sessions.mode == sessRename {
		chrome += 2
	} else if m.sessions.mode == sessConfirm {
		chrome++
	}
	return chrome
}

// sessVisible is how many entries fit the viewport. Stateless math
// shared by keys and render, so paging and clipping never disagree.
func (m *model) sessVisible() int {
	availH := m.termH - 3
	if m.ready {
		availH = m.vp.Height()
	}
	if n := (availH - m.sessChrome()) / 3; n > 0 {
		return n
	}
	return 1
}

// clampOffset keeps the cursor on screen: follow down, pull up,
// pin to bounds after delete re-scans.
func (m *model) clampOffset() {
	m.sessions.clamp(m.sessVisible())
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
	m.refreshNavigator()
	filter := textinput.New()
	filter.Prompt = "Search > "
	filter.SetVirtualCursor(false)
	style := filter.Styles()
	style.Cursor.Shape = tea.CursorUnderline
	filter.SetStyles(style)
	filter.Placeholder = "title or session ID"
	filter.CharLimit = 80
	filter.SetWidth(max(m.termW-14, 1))
	filter.Focus()
	m.sessions = &sessionsState{entries: m.nav.entries, all: m.nav.entries, root: m.sessionRoot(), filter: filter}
	m.pushOverlay(ovSessions)
	// The overlay shrinks the bottom bar (slim line, no input):
	// refit so the list gains the freed rows.
	m.fitBottom()
	m.refreshContent()
}

func (m *model) closeSessions() {
	m.sessions = nil
	m.resetNote()
	m.popOverlay()
	m.fitBottom()
	m.refreshContent()
	if m.follow {
		m.vp.GotoBottom()
	}
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
// Search accepts typing; ctrl+r/ctrl+d are list commands — except rename mode, where the
// borrowed note input owns every key but esc/enter, like gsReject.
func (m *model) sessionsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	s := m.sessions
	if msg.String() == "ctrl+c" || msg.String() == "ctrl+q" {
		m.quit = true
		m.cancel()
		return m, tea.Quit
	}
	if s.mode == sessRename {
		switch msg.String() {
		case "enter":
			m.commitRename()
			return m, nil
		case "esc":
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
	key := msg.String()
	if s.filter.Focused() {
		if key == "r" || key == "R" || key == "d" || key == "D" {
			key = ""
		}
	}
	switch key {
	case "esc":
		m.closeSessions()
		return m, nil
	case "enter":
		return m, m.openSelected()
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
	case "r", "R", "ctrl+r":
		if len(s.entries) == 0 {
			return m, nil
		}
		s.flash = ""
		s.mode = sessRename
		m.note.Prompt = "name> "
		m.note.CharLimit = 80
		m.note.SetValue("")
		return m, m.note.Focus()
	case "d", "D", "ctrl+d":
		if len(s.entries) == 0 {
			return m, nil
		}
		s.flash = ""
		s.mode = sessConfirm
		return m, nil
	}
	if s.filter.Focused() {
		before := s.filter.Value()
		var cmd tea.Cmd
		s.filter, cmd = s.filter.Update(msg)
		if before != s.filter.Value() {
			s.applyFilter()
		}
		return m, cmd
	}
	return m, nil
}

// openSelected switches to the selected session: snapshot loads,
// transcript rebuilds from history, follow-ups continue the
// conversation via the normal Resume path. Only at rest: switching
// mid-run would orphan the in-flight turn.
func (m *model) openSelected() tea.Cmd {
	s := m.sessions
	if len(s.entries) == 0 {
		return nil
	}
	if m.state != stDone {
		s.flash = "stop the run first"
		return nil
	}
	cur := s.entries[s.cursor]
	if cur.mode == "Mission" {
		s.flash = "Mission: run agent -tui -resume " + cur.dir
		return nil
	}
	stateFile := filepath.Join(cur.dir, "state.json")
	history, err := agent.LoadState(stateFile)
	if err != nil {
		s.flash = "Cannot read saved history: " + err.Error()
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
	oldPlan := m.plan
	if cur.mode == "Plan" {
		m.plan = true
	} else if cur.mode == "Act" {
		m.plan = false
	}
	sess, err := m.newSession(task, stateFile, nil)
	if err != nil {
		m.plan = oldPlan
		s.flash = "cannot open chat: " + err.Error()
		return nil
	}
	m.saveDraft()
	m.sess = sess
	m.stateFile = stateFile
	m.mission = false
	m.resetRunFacts()
	m.restoreDraft()
	mb := markerBlock("— opened " + cur.title + " —")
	mb.breakBefore = true
	m.blocks = append([]block{mb}, renderHistory(history)...)
	m.retryRun = nil
	m.runErr = nil
	m.answer = ""
	m.steps = 0
	m.tokens = 0
	m.state = stDone
	m.navFocused = false
	m.closeSessions()
	m.fitBottom()
	m.refreshContent()
	// Land at the top: a stale offset into replaced blocks shows
	// mid-history or blank. follow stays on so live turns behave.
	m.follow = true
	m.vp.GotoTop()
	m.restoreDraft()
	return m.input.Focus()
}

// renderHistory rebuilds the transcript from a loaded snapshot so a
// resumed or compacted session reads like it was never left. System
// prompts stay out; loop nudges retain their [harness] provenance as
// system notices. Tool cards re-attach their results by
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
				out = append(out, markerBlock(msg.Content))
				continue
			}
			if strings.TrimSpace(msg.Content) == "" && len(msg.Images) == 0 {
				continue
			}
			out = append(out, userBlock(displayInputContent(msg.Content)+imageSuffix(msg.Images)))
		case llm.RoleAssistant:
			out = append(out, assistantBlocks(msg.Content, msg.Reasoning)...)
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
		if err := workspace.WriteSessionTitle(cur.dir, ""); err != nil {
			s.flash = "rename failed: " + err.Error()
			return
		}
	} else if err := workspace.WriteSessionTitle(cur.dir, name); err != nil {
		s.flash = "rename failed: " + err.Error()
		return
	}
	s.flash = ""
	cur.title = name
	m.refreshNavigator()
	if s.all != nil {
		s.replaceEntries(m.nav.entries)
	}
}

// deleteSelected removes the session dir — title goes with it, so no
// orphan rows — then re-scans. Deleting the active session unloads it
// first (empty-chat state, then the dir is just files). Only at rest:
// unloading mid-run would orphan the in-flight turn.
func (m *model) deleteSelected() {
	s := m.sessions
	if len(s.entries) == 0 {
		s.mode = sessList
		return
	}
	cur := s.entries[s.cursor]
	if m.stateFile != "" && sameSession(cur.dir, m.stateFile) {
		if m.state != stDone {
			s.mode = sessList
			s.flash = "stop the run first"
			return
		}
		m.newChat()
	}
	target, err := filepath.Abs(cur.dir)
	root, rootErr := filepath.Abs(s.root)
	if err != nil || rootErr != nil || !strings.EqualFold(filepath.Dir(target), root) {
		s.mode, s.flash = sessList, "refusing to delete outside the session directory"
		return
	}
	if err := os.RemoveAll(target); err != nil {
		s.mode = sessList
		s.flash = "delete failed: " + err.Error()
		return
	}
	delete(m.drafts, filepath.Join(cur.dir, "state.json"))
	entries := listSessionsIn(s.root)
	m.refreshNavigator()
	if s.all != nil {
		s.replaceEntries(entries)
	} else {
		s.entries = entries
	}
	if s.cursor >= len(s.entries) && s.cursor > 0 {
		s.cursor--
	}
	m.clampOffset()
	s.mode = sessList
	s.flash = ""
}

// headerLine renders the top bar: brand + session title left, wall
// clock right. The brand animates while running (spinner) and sits
// solid amber at rest — motion alone tells working from idle, so no
// state word is needed anywhere near it.
func (m *model) headerLine() string {
	w := max(m.termW, 0)
	clock := time.Now().Format("15:04:05")
	if m.state != stDone {
		clock = "elapsed " + formatElapsed(m.elapsed)
	}
	title := m.activeSessionTitle()
	left := m.brand() + "// " + title
	if pad := w - lipgloss.Width(left) - len(clock); pad >= 2 {
		return left + strings.Repeat(" ", pad) + m.styles.dim.Render(clock)
	}
	return cellLine(left, w)
}

// activeSessionTitle names the open session for the header and the
// sessions bottom bar: stored title, then dir id; a dash when chatting
// without one yet. No history reads: the header renders every second.
func (m *model) activeSessionTitle() string {
	if m.stateFile == "" {
		return "New chat"
	}
	dir := filepath.Dir(m.stateFile)
	for _, e := range m.nav.entries {
		if sameSession(e.dir, m.stateFile) {
			return e.title
		}
	}
	if title := workspace.ReadSessionTitle(dir); title != "" {
		return title
	}
	return filepath.Base(dir)
}

// sessionsBar is the one-line bottom bar while the screen is open:
// which session is active, since the list shows everything but marks
// nothing. The chat input stays hidden — its keys belong to the list.
func (m *model) sessionsBar() string {
	return m.styles.dim.Render("Ctrl+R rename  Ctrl+D delete  Esc back  active: " + truncate(m.activeSessionTitle(), 60))
}

// sessionsView renders the list centered like a dialog: cursor-marked
// rows, per-mode hint, rename input or delete question when staged.
// Widths clamp to the viewport; the viewport clips the rest.
func (m *model) sessionsView() string { return m.browserView() }

// sessionRoot follows the chosen workspace, independently of the process cwd.
func (m *model) sessionRoot() string {
	root := m.wsRoot
	if m.ws != nil {
		root = m.ws.Root()
	}
	return filepath.Join(root, tasksRoot())
}
func sameSession(dir, stateFile string) bool {
	if stateFile == "" {
		return false
	}
	a, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	b, err := filepath.Abs(filepath.Dir(stateFile))
	return err == nil && strings.EqualFold(a, b)
}
func (m *model) refreshNavigator() {
	selected := ""
	if len(m.nav.entries) > 0 && m.nav.cursor < len(m.nav.entries) {
		selected = m.nav.entries[m.nav.cursor].dir
	}
	m.nav.entries = listSessionsIn(m.sessionRoot())
	for i, e := range m.nav.entries {
		if e.dir == selected {
			m.nav.cursor = i
			break
		}
	}
	m.nav.clamp(m.navigatorRows())
}
func (s *sessionsState) clamp(visible int) {
	s.cursor = max(0, min(s.cursor, len(s.entries)-1))
	if s.offset > s.cursor {
		s.offset = s.cursor
	}
	if s.cursor >= s.offset+visible {
		s.offset = s.cursor - visible + 1
	}
	s.offset = max(0, min(s.offset, max(len(s.entries)-visible, 0)))
}
func (s *sessionsState) applyFilter() {
	query := strings.ToLower(strings.TrimSpace(s.filter.Value()))
	s.entries = nil
	for _, e := range s.all {
		if strings.Contains(strings.ToLower(e.title+" "+e.id), query) {
			s.entries = append(s.entries, e)
		}
	}
	s.cursor, s.offset = 0, 0
}

func (s *sessionsState) replaceEntries(entries []sessionEntry) {
	index, selected := s.cursor, ""
	if len(s.entries) > 0 {
		selected = s.entries[s.cursor].dir
	}
	s.all = entries
	s.applyFilter()
	s.cursor = min(index, max(len(s.entries)-1, 0))
	for i, e := range s.entries {
		if e.dir == selected {
			s.cursor = i
			break
		}
	}
}

func (m *model) browserMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	s := m.sessions
	if s.mode != sessList {
		return m, nil
	}
	width := max(m.termW, 1)
	if width >= 90 {
		width = min(44, (width-3)/2)
	}
	if msg.Mouse().X >= width || msg.Mouse().Y < 3 || msg.Mouse().Y >= 3+m.vp.Height() {
		return m, nil
	}
	switch msg.Mouse().Button {
	case tea.MouseWheelUp:
		s.cursor--
	case tea.MouseWheelDown:
		s.cursor++
	case tea.MouseLeft:
		if !isMouseClick(msg) || msg.Mouse().Y < 6 {
			return m, nil
		}
		row := (msg.Mouse().Y - 6) / 3
		if row >= m.sessVisible() || s.offset+row >= len(s.entries) {
			return m, nil
		}
		s.cursor = s.offset + row
	default:
		return m, nil
	}
	m.clampOffset()
	return m, nil
}
