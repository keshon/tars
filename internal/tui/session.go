package tui

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/keshon/tars/internal/agent"
	"github.com/keshon/tars/internal/llm"
	"github.com/keshon/tars/internal/snapshot"
	"github.com/keshon/tars/internal/workspace"
)

// followUp sends a non-empty input line as a resumed turn;
// the saved transcript carries the conversation, so every follow-up is
// a continuation, not a fresh run. A /command runs locally instead.
func (m *model) followUp() (tea.Model, tea.Cmd) {
	original := m.input.Value()
	text := strings.TrimSpace(original)
	if strings.HasPrefix(text, "/") {
		m.input.SetValue("")
		m.fitInput()
		m.fitBottom()
		return m.command(text)
	}
	if text == "" {
		return m, nil
	}
	m.pushHistory(text)
	// @paths attach before anything else runs: a missing file stays
	// literal, a present non-image errors, an image strips out of the
	// text and rides the turn.
	text, images, err := splitAttachments(m.ws, text)
	if err != nil {
		m.appendBlock(errorBlock(err.Error()))
		return m, nil
	}
	// No session yet (opened without a task): the first line starts
	// a fresh run rather than resuming nothing. The echo lives in
	// startFresh (after its transcript reset) so every fresh-task
	// path — first line, /new — renders the opening task exactly once.
	m.input.SetValue("")
	m.fitInput()
	m.fitBottom()
	if m.sess == nil {
		if err := m.startFresh(text, images); err != nil {
			m.input.SetValue(original)
			m.fitInput()
			m.fitBottom()
			m.appendBlock(errorBlock("cannot start: " + err.Error()))
		}
		return m, nil
	}
	history, err := agent.LoadState(m.stateFile)
	if err != nil {
		m.input.SetValue(original)
		m.fitInput()
		m.fitBottom()
		m.appendBlock(errorBlock("cannot resume: " + err.Error()))
		return m, nil
	}
	if err := m.saveMode(); err != nil {
		m.input.SetValue(original)
		m.fitInput()
		m.fitBottom()
		m.appendBlock(errorBlock("cannot save mode: " + err.Error()))
		return m, nil
	}
	m.appendBlock(userBlock(text + imageSuffix(images)))
	m.startRun(func(runCtx context.Context) (string, error) {
		return m.sess.Resume(runCtx, history, text, images...)
	})
	return m, nil
}

// splitAttachments extracts @path tokens: an existing image file
// attaches (resolved absolute, stripped from the text), anything else
// stays literal. Emails and decorators never resolve, so they pass
// through untouched; a present non-image file is certainly a mistake
// and errors instead of riding along as text. Paths with spaces need
// quotes: @"my screenshot.png".
func splitAttachments(ws *workspace.Workspace, text string) (string, []string, error) {
	if ws == nil || !strings.Contains(text, "@") {
		return text, nil, nil
	}
	toks := splitTokens(text)
	var kept []string
	var images []string
	for _, tok := range toks {
		quoted := false
		raw := tok
		if strings.HasPrefix(tok, "@\"") && strings.HasSuffix(tok, "\"") && len(tok) > 3 {
			quoted = true
			raw = "@" + tok[2:len(tok)-1]
		}
		if !strings.HasPrefix(raw, "@") || len(raw) == 1 {
			kept = append(kept, tok)
			continue
		}
		full, err := ws.Resolve(raw[1:])
		if err != nil {
			return "", nil, fmt.Errorf("image %q escapes the workspace", raw)
		}
		fi, statErr := os.Stat(full)
		if statErr != nil {
			if quoted {
				return "", nil, fmt.Errorf("image %q not found", raw)
			}
			kept = append(kept, tok)
			continue
		}
		if fi.IsDir() {
			return "", nil, fmt.Errorf("%s is a directory, not an image", raw)
		}
		if !llm.IsImagePath(full) {
			return "", nil, fmt.Errorf("%s is not an image (png, jpg, webp, gif, bmp)", raw)
		}
		images = append(images, full)
	}
	return strings.Join(kept, " "), images, nil
}

// splitTokens splits on whitespace but keeps @"..." quoted spans
// whole: screenshot filenames love spaces.
func splitTokens(text string) []string {
	var toks []string
	var cur strings.Builder
	inQuotes := false
	flush := func() {
		if cur.Len() > 0 {
			toks = append(toks, cur.String())
			cur.Reset()
		}
	}
	for _, r := range text {
		switch {
		case r == '"' && !inQuotes && strings.HasSuffix(cur.String(), "@"):
			inQuotes = true
			cur.WriteRune(r)
		case r == '"' && inQuotes:
			inQuotes = false
			cur.WriteRune(r)
		case (r == ' ' || r == '\t' || r == '\n') && !inQuotes:
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return toks
}

// imageSuffix marks attached pictures on the echoed user block, so the
// transcript shows what the model saw alongside the text.
func imageSuffix(images []string) string {
	if len(images) == 0 {
		return ""
	}
	var b strings.Builder
	for _, p := range images {
		b.WriteString("\n[image: " + filepath.Base(p) + "]")
	}
	return b.String()
}

// startRun resets run counters and executes run in the background,
// delivering completion as doneMsg like the first run. Each run gets a
// fresh context: an interrupted run's cancelled context must never leak
// into the next turn.
func (m *model) startRun(run func(ctx context.Context) (string, error)) {
	m.cancel()
	// A run takes over: an open dialog — or sessions screen — would
	// shadow a transcript that is suddenly live again.
	if m.dialog != nil {
		m.closeDialog()
	}
	if m.sessions != nil {
		m.closeSessions()
	}
	// The turn closure is kept for /retry: re-invoking it re-attempts
	// the exact turn (follow-ups reuse their loaded history, so a
	// retry restarts from last known-good, not partial failure).
	// runErr clears: a new turn has no result yet, and /retry gates on it.
	// Live state clears too: a new turn starts with no partial text.
	m.alwaysMu.Lock()
	m.always = map[[2]string]bool{}
	m.alwaysMu.Unlock()
	m.retryRun = run
	m.runErr = nil
	m.live, m.liveCut, m.livePainted = "", false, 0
	m.firstToken = time.Time{}
	m.toolsUsed = 0
	m.filesTouched = map[string]bool{}
	// Unit-built models never set base; fall back instead of panicking
	// on a nil parent context.
	base := m.base
	if base == nil {
		base = context.Background()
	}
	runCtx, cancel := context.WithCancel(base)
	m.cancel = cancel
	m.ctx = runCtx
	m.state = stRunning
	m.navFocused = false
	m.refreshNavigator()
	m.input.Focus()
	m.fitInput()
	m.fitBottom()
	m.started = time.Now()
	m.steps = 0
	m.tokens = 0
	go func() {
		answer, err := run(runCtx)
		m.send(doneMsg{answer: answer, err: err})
	}()
}

// startInitial begins the argv task: it renders as the opening user
// block, then runs like any fresh turn. Without it the opening task
// is invisible — only follow-ups echo.
func (m *model) startInitial(task string, images []string) error {
	sess, err := m.newSession(task, m.stateFile, images)
	if err != nil {
		return err
	}
	m.sess = sess
	if err := m.saveMode(); err != nil {
		return err
	}
	m.appendBlock(userBlock(strings.TrimSpace(task) + imageSuffix(images)))
	m.startRun(func(runCtx context.Context) (string, error) {
		return sess.Run(runCtx)
	})
	return nil
}

// newChat resets to the empty-chat state: the session, transcript,
// and last-turn state go, the next submitted line starts a fresh run
// through the sess==nil path in followUp. Bare /new.
func (m *model) newChat() {
	if m.stateFile == "" && m.sess == nil {
		return
	}
	m.saveDraft()
	m.cancel()
	m.sess, m.stateFile, m.blocks = nil, "", nil
	m.mission = false
	m.resetRunFacts()
	m.state, m.navFocused = stDone, false
	m.restoreDraft()
	m.appendBlock(markerBlock("New chat · type a task to begin"))
	m.follow = true
	m.vp.GotoBottom()
}

// compactNow mechanically compacts the active session's saved history
// in place (/compact): the same compactHistory the loop uses, on
// demand instead of at the usage threshold. The transcript rebuilds
// from the compacted snapshot with a stats marker, so display and
// context agree on what was dropped. Follow-ups continue on the
// compacted history through the normal Resume path.
func (m *model) compactNow() (tea.Model, tea.Cmd) {
	if m.stateFile == "" {
		m.appendBlock(errorBlock("no active session to compact"))
		return m, nil
	}
	before, after, err := agent.CompactFile(m.stateFile, agent.DefaultCompactKeepSteps)
	if err != nil {
		m.appendBlock(errorBlock("cannot compact: " + err.Error()))
		return m, nil
	}
	if after >= before {
		m.appendBlock(markerBlock(fmt.Sprintf("nothing to compact: %d messages", before)))
		m.follow = true
		m.vp.GotoBottom()
		return m, nil
	}
	history, err := agent.LoadState(m.stateFile)
	if err != nil {
		m.appendBlock(errorBlock("cannot reload compacted history: " + err.Error()))
		return m, nil
	}
	mb := markerBlock(fmt.Sprintf("— compacted: %d → %d messages —", before, after))
	mb.breakBefore = true
	// Marker last, not first: the viewport lands at the bottom showing
	// the kept tail, which reads identical to before — the marker is
	// the only visible signal that anything happened.
	m.blocks = append(renderHistory(history), mb)
	m.refreshContent()
	// The point of manual compaction is seeing the shrunken transcript:
	// re-arm follow and jump to bottom even if the reader scrolled up,
	// or the marker lands below the fold and nothing visibly changed.
	m.follow = true
	m.vp.GotoBottom()
	return m, nil
}

// startFresh begins a new task in a fresh transcript and state dir,
// mirroring how the CLI namespaces one task per .tars/tasks/<id>.
func (m *model) startFresh(task string, images []string) error {
	sum := sha1.Sum([]byte(task + time.Now().String()))
	taskID := hex.EncodeToString(sum[:])[:8]
	taskDir := workspace.TaskDir(taskID)
	if m.ws != nil {
		taskDir = filepath.Join(m.ws.Root(), taskDir)
	}
	stateFile := filepath.Join(taskDir, "state.json")
	sess, err := m.newSession(task, stateFile, images)
	if err != nil {
		return err
	}
	if err := writeChatMode(stateFile, m.plan); err != nil {
		return err
	}
	if err := workspace.WriteSessionTitle(taskDir, workspace.TitleLine(task)); err != nil {
		return err
	}
	m.saveDraft()
	if m.stateFile == "" {
		delete(m.drafts, "")
	}
	m.sess, m.stateFile = sess, stateFile
	m.hist, m.histIdx, m.draft = []string{task}, 1, ""
	m.queued = ""
	m.blocks = nil
	nb := markerBlock("— new task —")
	nb.breakBefore = true
	m.appendBlock(nb)
	m.appendBlock(userBlock(task + imageSuffix(images)))
	if m.ws != nil {
		if snap := snapshot.Track(m.ws.Root(), filepath.Join(taskDir, "snapshots")); snap.Path == "" {
			m.appendBlock(markerBlock("Pre-run checkpoint unavailable; this run cannot be undone with -revert."))
		}
	}
	m.startRun(func(runCtx context.Context) (string, error) {
		return sess.Run(runCtx)
	})
	return nil
}
