package tui

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/keshon/tars/internal/agent"
	"github.com/keshon/tars/internal/workspace"
)

// followUp sends the input line as a resumed turn. Empty input quits;
// the saved transcript carries the conversation, so every follow-up is
// a continuation, not a fresh run. A /command runs locally instead.
func (m *model) followUp() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.input.Value())
	m.input.SetValue("")
	if strings.HasPrefix(text, "/") {
		return m.command(text)
	}
	if text == "" {
		m.quit = true
		m.cancel()
		return m, tea.Quit
	}
	m.pushHistory(text)
	// No session yet (opened without a task): the first line starts
	// a fresh run rather than resuming nothing. The echo lives in
	// startFresh (after its transcript reset) so every fresh-task
	// path — first line, /new — renders the opening task exactly once.
	if m.sess == nil {
		if err := m.startFresh(text); err != nil {
			m.appendBlock(errorBlock("cannot start: " + err.Error()))
		}
		return m, nil
	}
	history, err := agent.LoadState(m.stateFile)
	if err != nil {
		m.appendBlock(errorBlock("cannot resume: " + err.Error()))
		return m, nil
	}
	m.appendBlock(userBlock(text))
	m.startRun(func(runCtx context.Context) (string, error) {
		return m.sess.Resume(runCtx, history, text)
	})
	return m, nil
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
	m.retryRun = run
	m.runErr = nil
	m.live, m.liveCut, m.livePainted = "", false, 0
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
func (m *model) startInitial(task string) error {
	sess, err := m.newSession(task, m.stateFile)
	if err != nil {
		return err
	}
	m.sess = sess
	m.appendBlock(userBlock(strings.TrimSpace(task)))
	m.startRun(func(runCtx context.Context) (string, error) {
		return sess.Run(runCtx)
	})
	return nil
}

// newChat resets to the empty-chat state: the session, transcript,
// and last-turn state go, the next submitted line starts a fresh run
// through the sess==nil path in followUp. Bare /new.
func (m *model) newChat() {
	m.cancel()
	m.sess = nil
	m.stateFile = ""
	m.blocks = nil
	m.retryRun = nil
	m.runErr = nil
	m.answer = ""
	m.steps = 0
	m.tokens = 0
	m.state = stDone
	nb := markerBlock("— new task —")
	nb.breakBefore = true
	m.appendBlock(nb)
	m.input.SetValue("")
	m.fitBottom()
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
func (m *model) startFresh(task string) error {
	sum := sha1.Sum([]byte(task + time.Now().String()))
	taskID := hex.EncodeToString(sum[:])[:8]
	stateFile := filepath.Join(workspace.TaskDir(taskID), "state.json")
	_ = workspace.WriteSessionTitle(filepath.Dir(stateFile), workspace.TitleLine(task))
	sess, err := m.newSession(task, stateFile)
	if err != nil {
		return err
	}
	m.sess = sess
	m.stateFile = stateFile
	m.blocks = nil
	nb := markerBlock("— new task —")
	nb.breakBefore = true
	m.appendBlock(nb)
	m.appendBlock(userBlock(task))
	m.startRun(func(runCtx context.Context) (string, error) {
		return m.sess.Run(runCtx)
	})
	return nil
}
