package tui

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/keshon/tars/internal/agent"
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
	// a fresh run rather than resuming nothing.
	if m.sess == nil {
		if err := m.startFresh(text); err != nil {
			m.appendBlock(errorBlock("cannot start: " + err.Error()))
		} else {
			// Echo after startFresh: it resets the transcript, so
			// an earlier echo would not survive the wipe.
			m.appendBlock(userBlock("❯ " + text))
		}
		return m, nil
	}
	history, err := agent.LoadState(m.stateFile)
	if err != nil {
		m.appendBlock(errorBlock("cannot resume: " + err.Error()))
		return m, nil
	}
	m.appendBlock(userBlock("❯ " + text))
	m.startRun(func(runCtx context.Context) (string, error) {
		return m.sess.Resume(runCtx, history, text)
	})
	return m, nil
}

// command handles local slash commands. Anything unrecognized is
// reported, never sent to the model — a typo must not become a task.
func (m *model) command(text string) (tea.Model, tea.Cmd) {
	name, arg, _ := strings.Cut(strings.TrimPrefix(text, "/"), " ")
	m.pushHistory(text)
	switch strings.ToLower(name) {
	case "q", "quit":
		m.quit = true
		m.cancel()
		return m, tea.Quit
	case "help":
		m.appendBlock(markerBlock(
			"keys: q/ctrl+c abort · esc interrupts the run, stays in chat · y/n/a answer permission · enter submits · ctrl+o newline · up/down history · ctrl+g thinking\n" +
				"commands: /quit /help /new <task>"))
		return m, nil
	case "new":
		task := strings.TrimSpace(arg)
		if task == "" {
			m.appendBlock(errorBlock("usage: /new <task>"))
			return m, nil
		}
		if err := m.startFresh(task); err != nil {
			m.appendBlock(errorBlock("cannot start: " + err.Error()))
			return m, nil
		}
		return m, nil
	default:
		m.appendBlock(errorBlock("unknown command " + text + " (try /help)"))
		return m, nil
	}
}

// startRun resets run counters and executes run in the background,
// delivering completion as doneMsg like the first run. Each run gets a
// fresh context: an interrupted run's cancelled context must never leak
// into the next turn.
func (m *model) startRun(run func(ctx context.Context) (string, error)) {
	m.cancel()
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
	m.appendBlock(userBlock("❯ " + strings.TrimSpace(task)))
	m.startRun(func(runCtx context.Context) (string, error) {
		return sess.Run(runCtx)
	})
	return nil
}

// startFresh begins a new task in a fresh transcript and state dir,
// mirroring how the CLI namespaces one task per .agent/tasks/<id>.
func (m *model) startFresh(task string) error {
	sum := sha1.Sum([]byte(task + time.Now().String()))
	taskID := hex.EncodeToString(sum[:])[:8]
	stateFile := filepath.Join(".agent", "tasks", taskID, "state.json")
	sess, err := m.newSession(task, stateFile)
	if err != nil {
		return err
	}
	m.sess = sess
	m.stateFile = stateFile
	m.blocks = nil
	m.appendBlock(markerBlock("— new task —"))
	m.startRun(func(runCtx context.Context) (string, error) {
		return m.sess.Run(runCtx)
	})
	return nil
}
