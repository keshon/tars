// Package tui renders a run in the terminal: transcript, status, and
// gate prompts. It decides nothing — it renders api.Session events and
// answers gates through a GateHub, the same seam the stdio server uses.
//
// Scope is deliberate: direct runs only (mission stays on the CLI and
// -serve), no themes, no images, no autocomplete. Print mode remains
// the default; the TUI is opt-in.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/keshon/tars/internal/agent"
	"github.com/keshon/tars/internal/api"
	"github.com/keshon/tars/internal/audit"
	"github.com/keshon/tars/internal/llm"
	"github.com/keshon/tars/internal/roles"
)

// Config wires one TUI run. Env carries the full harness (client,
// workspace, tools, policy, budgets); gate behavior comes from the TUI,
// so Env.Gate is overridden, never read.
type Config struct {
	Env       roles.Env
	Task      string
	StateFile string
	Verify    func(ctx context.Context) (output string, ok bool)
	// AuditPath, when set, appends gate decisions as JSONL (see audit).
	AuditPath string
}

type runState int

const (
	stRunning runState = iota
	stPermission
	stAsk
	stDone
)

type eventMsg api.Event

type doneMsg struct {
	answer string
	err    error
}

type tickMsg time.Time

type model struct {
	vp    viewport.Model
	input textarea.Model
	// hist is the submitted-line history (cap 50, consecutive dedup);
	// histIdx points past the end when typing fresh input. draft holds
	// the unsent line parked while browsing history.
	hist    []string
	histIdx int
	draft   string
	events  chan api.Event
	hub     *api.GateHub
	cancel  context.CancelFunc
	ctx     context.Context
	// base spawns one context per run: an interrupted run's cancelled
	// context must never leak into the next turn.
	base   context.Context
	sess   *api.Session
	blocks []block
	state  runState
	gate   string
	steps  int
	// maxSteps is the run's step budget for the meter: the loop default,
	// since the Interactive role never overrides it. Display only — the
	// loop enforces its own copy.
	maxSteps int
	started  time.Time
	elapsed  time.Duration
	ready    bool
	answer   string
	runErr   error
	styles   styles
	quit     bool
	// interrupted marks a run stopped by esc rather than completion;
	// its doneMsg renders neutrally instead of as an error.
	interrupted bool
	// stateFile locates the saved transcript for follow-ups.
	stateFile string
	// newSession builds a session for a fresh task (first run and
	// /new). Follow-ups reuse the running session via Resume.
	newSession func(task, stateFile string) (*api.Session, error)
	// retryRun is the last turn's closure, kept so /retry can
	// re-attempt it exactly. Set by startRun, gated by runErr.
	retryRun func(ctx context.Context) (string, error)
	// send delivers out-of-band messages (run completion) to the
	// program from worker goroutines. Set by Run once tea exists.
	send func(tea.Msg)
	// termW/termH are the last reported terminal dimensions, kept so
	// state transitions can resize the viewport to fit the bottom bar.
	termW int
	termH int
	// tokens is the latest backend-reported prompt size; limit is the
	// context window from Env. Together they drive the status meter.
	tokens int
	limit  int
	// tokensEst marks estimated numbers (streaming backends that report
	// no usage block); the meter prefixes "~" so estimates never pose
	// as measurements.
	tokensEst bool
	// follow tracks viewport stickiness: new blocks auto-scroll only
	// while the user hasn't scrolled away. Any manual scroll re-arms
	// on reaching the bottom; End always re-arms.
	follow bool
	// compact collapses verbose blocks: thinking summaries and long
	// tool results (10-line preview). Toggled by the think key (see
	// thinkToggleHint); render-only, history keeps everything either
	// way. On by default: details on demand, never a flood.
	compact bool
	// live accumulates streamed content chunks for the live answer
	// block; liveCut marks the 64KB truncation tail; lastLive gates
	// repaints (see livePaintInterval).
	live     string
	liveCut  bool
	lastLive time.Time
	// livePainted is the byte length painted at the last live repaint;
	// deltas before it are settled, after it pending. Cleared with live.
	livePainted int
	// firstToken stamps the first streamed chunk of the run: time to
	// first word beside elapsed time. Zero until streaming starts;
	// cleared with live at every turn boundary.
	firstToken time.Time
	// toolsUsed counts tool calls this run; filesTouched collects
	// distinct path args seen. Both reset in startRun with steps and
	// tokens, and feed the status counters.
	toolsUsed    int
	filesTouched map[string]bool
	// partLines holds rendered line counts per transcript part, kept so
	// pure re-renders (toggles, resizes) can hold the reader's content
	// position instead of its offset. See refreshContent.
	partLines []int
	// overlays is the overlay stack (overlay.go); gates are its first
	// client. gstage/gateTool/gateResource describe the open permission
	// gate; note is the reject-note input; always remembers run-local
	// always-allow pairs confirmed in this process.
	overlays     []overlayFrame
	gstage       gateStage
	gateTool     string
	gateResource string
	note         textinput.Model
	always       map[[2]string]bool
	// dialog is the open modal, if any (dialog.go). While open it
	// replaces the transcript body and eats all keys but close/quit.
	dialog *dialog
	// sessions is the open sessions screen, if any (sessions.go): a
	// browsable past-task list with rename/delete. Like a dialog it
	// replaces the body and owns its keys; unlike a dialog it borrows
	// the note input for rename entry.
	sessions *sessionsState
	// Status facts snapshotted from cfg at construction for /status:
	// backend/workspace identity and budgets the run was given.
	backendKind string
	modelName   string
	wsRoot      string
	thinkBudget int
	mcpCount    int
	policyRules int
}

// gateEvent reshapes hub fields into the TUI's event: kind, prompt,
// and the permission call identity gates render from. A field dropped
// here never reaches handleEvent: the always-confirm scope went blank
// live for exactly this reason (tests inject events directly and never
// saw it).
func gateEvent(name string, fields map[string]any) api.Event {
	prompt, _ := fields["prompt"].(string)
	tool, _ := fields["tool"].(string)
	resource, _ := fields["resource"].(string)
	return api.Event{Name: name, Fields: map[string]any{
		"kind": fields["kind"], "prompt": prompt, "tool": tool, "resource": resource,
	}}
}

// withoutPrintHooks strips the CLI print hooks from Env for fullscreen
// runs: stepPrinter-style callbacks (steps, findings, nudges) write to
// stdout, racing the alt-screen renderer (stale status rows plus leaked
// "[step N]" lines that heal only on resize). The TUI reads the event
// stream, never the hooks; no-ops keep every call site safe without
// auditing each one.
func withoutPrintHooks(env roles.Env) roles.Env {
	env.OnDelta = func(string) {}
	env.OnStep = func(string, int, llm.Message) {}
	env.OnToolResult = func(string, string) {}
	env.OnUsage = func(int, llm.Usage) {}
	env.OnFinding = nil
	env.OnNudge = nil
	return env
}

// envMaxSteps mirrors the loop default: Env.MaxSteps set by -max-steps,
// otherwise the agent default. Display only — like the other status
// facts it is snapshotted once; the live meter follows max_steps events.
func envMaxSteps(env roles.Env) int {
	if env.MaxSteps > 0 {
		return env.MaxSteps
	}
	return agent.DefaultMaxSteps
}

// Run executes the task under the TUI and returns the final answer.
// The terminal is restored on return, however the run ends.
func Run(ctx context.Context, cfg Config) (string, error) {
	eventsCh := make(chan api.Event, 256)
	hub := api.NewGateHub(func(name string, fields map[string]any) {
		eventsCh <- gateEvent(name, fields)
	})

	runCtx, cancel := context.WithCancel(ctx)
	m := &model{
		events:    eventsCh,
		hub:       hub,
		cancel:    cancel,
		ctx:       runCtx,
		base:      ctx,
		started:   time.Now(),
		styles:    defaultStyles(),
		limit:     cfg.Env.ContextLimit,
		maxSteps:  envMaxSteps(cfg.Env),
		follow:    true,
		compact:   true,
		stateFile: cfg.StateFile,
		note:      newNote(),
		always:    map[[2]string]bool{},
		// Status facts come from cfg once: the TUI never re-reads Env.
		backendKind: cfg.Env.BackendKind,
		modelName:   cfg.Env.Model,
		wsRoot:      wsRootOf(cfg.Env.WS),
		thinkBudget: cfg.Env.ReasoningBudget,
		mcpCount:    len(cfg.Env.MCPTools),
		policyRules: len(cfg.Env.Policy.Rules),
	}
	m.input = newInput()
	m.input.Prompt = "> "

	env := withoutPrintHooks(cfg.Env)
	env.Gate = audit.Hook(cfg.AuditPath, "tui", m.gateHook(hub, runCtx, cfg.Env.WS))
	m.newSession = func(task, stateFile string) (*api.Session, error) {
		return api.New(api.Config{
			Env:       env,
			Task:      task,
			StateFile: stateFile,
			Verify:    cfg.Verify,
			Answer:    hub.Suspender(),
			OnEvent: func(ev api.Event) {
				eventsCh <- ev
			},
		})
	}
	prog := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	m.send = prog.Send
	if strings.TrimSpace(cfg.Task) != "" {
		if err := m.startInitial(cfg.Task); err != nil {
			cancel()
			return "", err
		}
	} else {
		// No initial task: open chatting, not running. The first
		// submitted line starts a fresh run like /new, so the TUI
		// never requires argv.
		m.state = stDone
		m.appendBlock(markerBlock("type a task to begin"))
		m.input.Focus()
	}

	final, err := prog.Run()
	if err != nil {
		cancel()
		return "", err
	}
	fm, ok := final.(*model)
	if !ok {
		return "", fmt.Errorf("tui: unexpected final model")
	}
	if fm.quit {
		cancel()
		return "", context.Canceled
	}
	return fm.answer, fm.runErr
}
