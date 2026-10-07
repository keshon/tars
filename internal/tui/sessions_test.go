package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/keshon/tars/internal/agent"
	"github.com/keshon/tars/internal/api"
	"github.com/keshon/tars/internal/llm"
	"github.com/keshon/tars/internal/workspace"
)

// writeSessionState stores a history as state.json in root/<id>/,
// mirroring the on-disk session layout.
func writeSessionState(t *testing.T, root, id string, history []llm.Message) string {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(history)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func userHistory(tasks ...string) []llm.Message {
	var out []llm.Message
	for _, task := range tasks {
		out = append(out, llm.Message{Role: llm.RoleUser, Content: task})
		out = append(out, llm.Message{Role: llm.RoleAssistant, Content: "done"})
	}
	return out
}

// chdirSessions points the relative tasks root at tmp: sessions code
// resolves like the CLI (cwd-joined), so tests relocate cwd instead.
func chdirSessions(t *testing.T, tmp string) {
	t.Helper()
	t.Chdir(tmp)
	if err := os.MkdirAll(filepath.Join(".tars", "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestListSessions_TitlesAndOrder(t *testing.T) {
	tmp := t.TempDir()
	chdirSessions(t, tmp)
	tasks := filepath.Join(".tars", "tasks")
	writeSessionState(t, tasks, "aaa", userHistory("fix the login bug"))
	oldDir := writeSessionState(t, tasks, "bbb", userHistory("add dark mode"))
	// bbb is older: newest-first must put aaa on top regardless of id.
	ago := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(filepath.Join(oldDir, "state.json"), ago, ago); err != nil {
		t.Fatal(err)
	}

	entries := listSessions()
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	if entries[0].id != "aaa" || entries[0].title != "fix the login bug" {
		t.Fatalf("first = %+v, want aaa/fix the login bug", entries[0])
	}
	if entries[1].id != "bbb" || entries[1].title != "add dark mode" {
		t.Fatalf("second = %+v, want bbb/add dark mode", entries[1])
	}
	if entries[0].msgs != 2 {
		t.Fatalf("msgs = %d, want 2", entries[0].msgs)
	}
}

// Stored titles win over derived text; corrupt snapshots still appear
// (a broken session is one the user may want to delete).
func TestListSessions_TitleFileOverridesCorruptSurvives(t *testing.T) {
	tmp := t.TempDir()
	chdirSessions(t, tmp)
	tasks := filepath.Join(".tars", "tasks")
	dir := writeSessionState(t, tasks, "aaa", userHistory("original task text"))
	if err := os.WriteFile(filepath.Join(dir, "title"), []byte("my label\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	badDir := filepath.Join(tasks, "bbb")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "state.json"), []byte("{corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries := listSessions()
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	byID := map[string]sessionEntry{}
	for _, e := range entries {
		byID[e.id] = e
	}
	if byID["aaa"].title != "my label" {
		t.Fatalf("aaa title = %q, want stored title", byID["aaa"].title)
	}
	if byID["bbb"].title != "bbb" {
		t.Fatalf("bbb title = %q, want id fallback", byID["bbb"].title)
	}
}

func sessionsTestModel(t *testing.T, entries []sessionEntry) *model {
	t.Helper()
	m := sizeModel(t, testModel())
	m.state = stDone
	m.sessions = &sessionsState{entries: entries, root: tasksRoot()}
	// Resume needs a session object for follow-ups; api.New validates
	// a full Env, so tests stub the constructor (production always
	// wires the real one in Run).
	m.newSession = func(task, stateFile string, images []string) (*api.Session, error) {
		return &api.Session{}, nil
	}
	return m
}

func TestSessions_RenameWritesTitleFile(t *testing.T) {
	tmp := t.TempDir()
	chdirSessions(t, tmp)
	dir := writeSessionState(t, filepath.Join(".tars", "tasks"), "aaa", userHistory("original text"))
	m := sessionsTestModel(t, listSessions())

	updated, _ := m.Update(keyRunes('r'))
	mm := updated.(*model)
	if mm.sessions.mode != sessRename {
		t.Fatalf("mode = %v, want sessRename", mm.sessions.mode)
	}
	mm.note.SetValue("my session")
	updated, _ = mm.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm = updated.(*model)
	if mm.sessions.mode != sessList {
		t.Fatalf("mode = %v, want back to list", mm.sessions.mode)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "title"))
	if err != nil {
		t.Fatalf("title file missing: %v", err)
	}
	if string(raw) != "my session\n" {
		t.Fatalf("title file = %q", raw)
	}
	if mm.sessions.entries[0].title != "my session" {
		t.Fatalf("row title = %q, want refreshed rename", mm.sessions.entries[0].title)
	}
}

func TestSessions_EmptyRenameClearsTitle(t *testing.T) {
	tmp := t.TempDir()
	chdirSessions(t, tmp)
	tasks := filepath.Join(".tars", "tasks")
	dir := writeSessionState(t, tasks, "aaa", userHistory("original text"))
	if err := os.WriteFile(filepath.Join(dir, "title"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := sessionsTestModel(t, listSessions())

	updated, _ := m.Update(keyRunes('r'))
	mm := updated.(*model)
	mm.note.SetValue("  ")
	updated, _ = mm.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm = updated.(*model)
	if _, err := os.Lstat(filepath.Join(dir, "title")); !os.IsNotExist(err) {
		t.Fatalf("title file still present after clearing")
	}
	if mm.sessions.entries[0].title != "original text" {
		t.Fatalf("row title = %q, want derived title back", mm.sessions.entries[0].title)
	}
}

func TestSessions_DeleteRemovesDir(t *testing.T) {
	tmp := t.TempDir()
	chdirSessions(t, tmp)
	tasks := filepath.Join(".tars", "tasks")
	writeSessionState(t, tasks, "aaa", userHistory("one"))
	writeSessionState(t, tasks, "bbb", userHistory("two"))
	m := sessionsTestModel(t, listSessions())
	// Select the second row, whichever session sorted there.
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	mm := updated.(*model)
	victim := mm.sessions.entries[mm.sessions.cursor]
	updated, _ = mm.Update(keyRunes('d'))
	mm = updated.(*model)
	if mm.sessions.mode != sessConfirm {
		t.Fatalf("mode = %v, want sessConfirm", mm.sessions.mode)
	}
	updated, _ = mm.Update(keyRunes('y'))
	mm = updated.(*model)
	if _, err := os.Lstat(victim.dir); !os.IsNotExist(err) {
		t.Fatalf("session dir still present after delete")
	}
	if len(mm.sessions.entries) != 1 {
		t.Fatalf("entries = %+v, want one survivor", mm.sessions.entries)
	}
	if mm.sessions.entries[0].dir == victim.dir {
		t.Fatalf("deleted session still listed")
	}
}

func TestSessions_DeleteActiveUnloadsFirst(t *testing.T) {
	tmp := t.TempDir()
	chdirSessions(t, tmp)
	dir := writeSessionState(t, filepath.Join(".tars", "tasks"), "aaa", userHistory("live"))
	m := sessionsTestModel(t, listSessions())
	m.stateFile = filepath.Join(dir, "state.json")
	m.sess = &api.Session{}
	m.blocks = []block{answerBlock("old transcript")}

	updated, _ := m.Update(keyRunes('d'))
	mm := updated.(*model)
	updated, _ = mm.Update(keyRunes('y'))
	mm = updated.(*model)
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatalf("session dir still present after delete")
	}
	if mm.sess != nil || mm.stateFile != "" {
		t.Fatal("active session not unloaded before delete")
	}
	if len(mm.sessions.entries) != 0 {
		t.Fatalf("entries = %+v, want empty", mm.sessions.entries)
	}
}

func TestSessions_DeleteActiveRefusesWhileRunning(t *testing.T) {
	tmp := t.TempDir()
	chdirSessions(t, tmp)
	dir := writeSessionState(t, filepath.Join(".tars", "tasks"), "aaa", userHistory("live"))
	m := sessionsTestModel(t, listSessions())
	m.state = stRunning
	m.stateFile = filepath.Join(dir, "state.json")

	updated, _ := m.Update(keyRunes('d'))
	mm := updated.(*model)
	updated, _ = mm.Update(keyRunes('y'))
	mm = updated.(*model)
	if _, err := os.Lstat(dir); err != nil {
		t.Fatalf("session deleted mid-run: %v", err)
	}
	if mm.sessions.flash == "" {
		t.Fatal("no refusal message shown")
	}
}

// Enter switches sessions: stateFile swaps, the transcript rebuilds
// from history, follow-ups continue via Resume. Harness notices remain
// visible; the system prompt stays out of the conversation.
func TestSessions_EnterResumesWithHistory(t *testing.T) {
	tmp := t.TempDir()
	chdirSessions(t, tmp)
	history := []llm.Message{
		{Role: llm.RoleSystem, Content: "system prompt"},
		{Role: llm.RoleUser, Content: "[harness] verify yourself"},
		{Role: llm.RoleUser, Content: "build the widget"},
		{Role: llm.RoleAssistant, Content: "on it", ToolCalls: []llm.ToolCall{
			{ID: "c1", Name: "list_files", Arguments: json.RawMessage(`{"path":"."}`)},
		}},
		{Role: llm.RoleTool, ToolCallID: "c1", Content: "a.go\nb.go\n"},
		{Role: llm.RoleAssistant, Content: "widget built"},
	}
	dir := writeSessionState(t, filepath.Join(".tars", "tasks"), "aaa", history)
	m := sessionsTestModel(t, listSessions())

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm := updated.(*model)
	if mm.sessions != nil {
		t.Fatal("screen did not close after resume")
	}
	if mm.stateFile != filepath.Join(dir, "state.json") {
		t.Fatalf("stateFile = %q, want the resumed snapshot", mm.stateFile)
	}
	if mm.sess == nil {
		t.Fatal("no session built for follow-ups")
	}
	var texts []string
	for _, b := range mm.blocks {
		texts = append(texts, b.text, b.result)
	}
	joined := strings.Join(texts, "\n")
	for _, want := range []string{"opened", "[harness] verify yourself", "build the widget", "on it", "list_files", "a.go", "widget built"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("transcript missing %q:\n%s", want, joined)
		}
	}
	for _, banned := range []string{"system prompt"} {
		if strings.Contains(joined, banned) {
			t.Fatalf("transcript leaked %q", banned)
		}
	}
}

// Resume needs rest: switching mid-run would orphan the in-flight turn.
func TestSessions_EnterRefusesWhileRunning(t *testing.T) {
	tmp := t.TempDir()
	chdirSessions(t, tmp)
	writeSessionState(t, filepath.Join(".tars", "tasks"), "aaa", userHistory("task"))
	m := sessionsTestModel(t, listSessions())
	m.state = stRunning

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm := updated.(*model)
	if mm.sessions == nil {
		t.Fatal("screen closed despite refused resume")
	}
	if mm.sessions.flash == "" {
		t.Fatal("no refusal message shown")
	}
}

// The list scrolls under a stationary viewport: rows past capacity
// are unreachable without it, which is how a long list shipped
// unclipped-but-unbrowsable.
func TestSessions_ListScrollsToCursor(t *testing.T) {
	tmp := t.TempDir()
	chdirSessions(t, tmp)
	tasks := filepath.Join(".tars", "tasks")
	for i := 0; i < 20; i++ {
		id := "s" + twoDigits(i)
		writeSessionState(t, tasks, id, userHistory("task "+id))
	}
	m := sessionsTestModel(t, listSessions())
	visible := m.sessVisible()
	if visible >= 20 {
		t.Fatalf("visible = %d, test needs overflow", visible)
	}
	for i := 0; i < 15; i++ {
		updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		m = updated.(*model)
	}
	if m.sessions.cursor != 15 {
		t.Fatalf("cursor = %d, want 15", m.sessions.cursor)
	}
	if m.sessions.offset != 15-visible+1 {
		t.Fatalf("offset = %d, want %d", m.sessions.offset, 15-visible+1)
	}
	// Order-agnostic: the window holds exactly the visible slice
	// around the cursor, whatever mtime order put there.
	selected := m.sessions.entries[15].title
	hidden := m.sessions.entries[0].title
	view := m.View().Content
	if strings.Contains(view, hidden) {
		t.Fatalf("top entry %q still rendered past the scroll window", hidden)
	}
	if !strings.Contains(view, selected) {
		t.Fatalf("selected entry %q not rendered", selected)
	}
	if !strings.Contains(view, "16/20") {
		t.Fatal("position missing from hint line")
	}
}

func TestSessions_PageKeysMoveByViewport(t *testing.T) {
	tmp := t.TempDir()
	chdirSessions(t, tmp)
	tasks := filepath.Join(".tars", "tasks")
	for i := 0; i < 20; i++ {
		id := "s" + twoDigits(i)
		writeSessionState(t, tasks, id, userHistory("task "+id))
	}
	m := sessionsTestModel(t, listSessions())
	visible := m.sessVisible()

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	m = updated.(*model)
	if m.sessions.cursor != visible {
		t.Fatalf("cursor = %d, want one page (%d)", m.sessions.cursor, visible)
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	m = updated.(*model)
	if m.sessions.cursor != 0 || m.sessions.offset != 0 {
		t.Fatalf("cursor/offset = %d/%d, want 0/0", m.sessions.cursor, m.sessions.offset)
	}
}

func twoDigits(i int) string {
	if i < 10 {
		return "0" + string(rune('0'+i))
	}
	return "1" + string(rune('0'+i-10))
}

func TestSessions_CommandOpensAndEscCloses(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	updated, _ := m.command("/sessions")
	mm := updated.(*model)
	if mm.sessions == nil {
		t.Fatal("/sessions did not open the screen")
	}
	// The overlay owns the bottom bar (slim line, no input): the list
	// gains the freed rows, and closing hands them back.
	if mm.vp.Height() != 24-7-2 {
		t.Fatalf("sessions viewport height = %d, want 15", mm.vp.Height())
	}
	updated, _ = mm.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	mm = updated.(*model)
	if mm.sessions != nil {
		t.Fatal("esc did not close the screen")
	}
	if mm.vp.Height() != 24-7-2 {
		t.Fatalf("closed viewport height = %d, want 15", mm.vp.Height())
	}
}

// A terminal resize refits the viewport: stale heights clip content
// by the delta, which reads as a one-line scroll shortfall.
func TestViewportRefitsOnResize(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	mm := updated.(*model)
	if mm.vp.Height() != 30-7-2 {
		t.Fatalf("resized viewport height = %d, want 21", mm.vp.Height())
	}
}

// Resume lands at the top of the rebuilt transcript: a stale offset
// into replaced blocks shows mid-history or blank.
func TestSessions_ResumeShowsTop(t *testing.T) {
	tmp := t.TempDir()
	chdirSessions(t, tmp)
	writeSessionState(t, filepath.Join(".tars", "tasks"), "aaa", compactTestHistory(15))
	m := sessionsTestModel(t, listSessions())
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm := updated.(*model)
	if mm.sessions != nil {
		t.Fatal("screen did not close after resume")
	}
	if !mm.follow || mm.vp.YOffset() != 0 {
		t.Fatal("resume must land at the top with follow on")
	}
}

func TestSessions_HelpListsCommand(t *testing.T) {
	found := false
	for _, s := range helpSections() {
		for _, r := range s.rows {
			if r[0] == "/sessions" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("help has no /sessions row — command will rot undocumented")
	}
}

func compactTestHistory(n int) []llm.Message {
	history := []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "big task"},
	}
	for i := 0; i < n; i++ {
		history = append(history,
			llm.Message{Role: llm.RoleAssistant, Content: "step done"},
			llm.Message{Role: llm.RoleUser, Content: "keep going"},
		)
	}
	return history
}

// /compact shrinks the snapshot in place and rebuilds the transcript
// from it: display and context agree on what was dropped.
func TestCommand_CompactShrinksAndRerenders(t *testing.T) {
	tmp := t.TempDir()
	chdirSessions(t, tmp)
	dir := writeSessionState(t, filepath.Join(".tars", "tasks"), "aaa", compactTestHistory(15))
	m := sizeModel(t, testModel())
	m.state = stDone
	m.stateFile = filepath.Join(dir, "state.json")
	m.input.SetValue("/compact")

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm := updated.(*model)
	if len(mm.blocks) == 0 || !strings.Contains(mm.blocks[len(mm.blocks)-1].text, "compacted:") {
		t.Fatalf("blocks = %+v, want a compaction marker last", mm.blocks)
	}
	loaded, err := agent.LoadState(mm.stateFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) >= 32 {
		t.Fatalf("snapshot still holds %d messages", len(loaded))
	}
	// Follow-ups continue on the compacted history, not the old one.
	if mm.stateFile != filepath.Join(dir, "state.json") {
		t.Fatal("compact must not switch sessions")
	}
}

func TestCommand_CompactNeedsSession(t *testing.T) {
	m := sizeModel(t, testModel())
	m.state = stDone
	m.input.SetValue("/compact")
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm := updated.(*model)
	if len(mm.blocks) == 0 || !strings.Contains(mm.blocks[0].text, "no active session") {
		t.Fatalf("blocks = %+v, want the no-session error", mm.blocks)
	}
}

func TestCommand_CompactNothingToDrop(t *testing.T) {
	tmp := t.TempDir()
	chdirSessions(t, tmp)
	dir := writeSessionState(t, filepath.Join(".tars", "tasks"), "aaa", userHistory("small"))
	m := sizeModel(t, testModel())
	m.state = stDone
	m.stateFile = filepath.Join(dir, "state.json")
	m.input.SetValue("/compact")
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm := updated.(*model)
	if len(mm.blocks) != 1 || !strings.Contains(mm.blocks[0].text, "nothing to compact") {
		t.Fatalf("blocks = %+v, want the no-op marker", mm.blocks)
	}
}

// Compacting jumps to the bottom even when scrolled up: the marker and
// shrunken tail must be on screen, or the command looks like a no-op.
func TestCommand_CompactForcesBottom(t *testing.T) {
	tmp := t.TempDir()
	chdirSessions(t, tmp)
	dir := writeSessionState(t, filepath.Join(".tars", "tasks"), "aaa", compactTestHistory(15))
	m := sizeModel(t, testModel())
	m.state = stDone
	m.stateFile = filepath.Join(dir, "state.json")
	m.vp.GotoTop()
	m.follow = false
	m.input.SetValue("/compact")
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm := updated.(*model)
	if !mm.follow || !mm.vp.AtBottom() {
		t.Fatal("compact must re-arm follow and jump to bottom")
	}
}

// @paths attach existing images and strip from the text; anything
// else stays literal (emails must survive), except a present
// non-image file, which is certainly a mistake.
func TestSplitAttachments(t *testing.T) {
	dir := t.TempDir()
	ws, err := workspace.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "shot.png"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	clean, images, err := splitAttachments(ws, "look @shot.png please")
	if err != nil {
		t.Fatal(err)
	}
	if clean != "look please" || len(images) != 1 || filepath.Base(images[0]) != "shot.png" {
		t.Fatalf("clean=%q images=%v", clean, images)
	}

	clean, images, err = splitAttachments(ws, "mail me@home and @missing.png")
	if err != nil {
		t.Fatal(err)
	}
	if clean != "mail me@home and @missing.png" || len(images) != 0 {
		t.Fatalf("literals must survive: %q %v", clean, images)
	}

	// Quoted spans survive spaces: Windows screenshot names.
	if err := os.WriteFile(filepath.Join(dir, "my shot.png"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	clean, images, err = splitAttachments(ws, `describe @"my shot.png" now`)
	if err != nil {
		t.Fatal(err)
	}
	if clean != "describe now" || len(images) != 1 || filepath.Base(images[0]) != "my shot.png" {
		t.Fatalf("quoted: clean=%q images=%v", clean, images)
	}
	if _, _, err = splitAttachments(ws, `look @"nope.png"`); err == nil {
		t.Fatal("quoted missing file must error, not ride as text")
	}

	if clean, _, err = splitAttachments(ws, "read @notes.txt"); err != nil || !strings.Contains(clean, "Workspace file \"notes.txt\"") {
		t.Fatalf("text reference missing: %q %v", clean, err)
	}
	if _, _, err = splitAttachments(nil, "look @shot.png"); err != nil {
		t.Fatal(err)
	}
}

// A fresh follow-up with @path reaches the session constructor.

// Resumed image turns render their marker from history.
func TestRenderHistory_ShowsImageMarkers(t *testing.T) {
	history := []llm.Message{
		{Role: llm.RoleUser, Content: "look", Images: []string{filepath.Join("d", "shot.png")}},
	}
	blocks := renderHistory(history)
	if len(blocks) != 1 || !strings.Contains(blocks[0].text, "[image: shot.png]") {
		t.Fatalf("blocks = %+v", blocks)
	}
}
