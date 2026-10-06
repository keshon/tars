// Package tui renders a run in the terminal: transcript, status, and
// gate prompts. It decides nothing — it renders api.Session events and
// answers gates through a GateHub, the same seam the stdio server uses.
//
// Print mode remains the default; -tui renders direct, plan, and mission runs.
package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/keshon/tars/internal/agent"
	"github.com/keshon/tars/internal/api"
	"github.com/keshon/tars/internal/audit"
	"github.com/keshon/tars/internal/events"
	"github.com/keshon/tars/internal/llm"
	"github.com/keshon/tars/internal/roles"
	"github.com/keshon/tars/internal/workspace"
)

// Config wires one TUI run. Env carries the full harness (client,
// workspace, tools, policy, budgets); gate behavior comes from the TUI,
// so Env.Gate is overridden, never read.
type Config struct {
	Plan         bool
	History      []llm.Message
	ResumeAnswer string
	Mission      func(context.Context, roles.Env, agent.Suspender, *events.Emitter) error
	Env          roles.Env
	Task         string
	StateFile    string
	Verify       func(ctx context.Context) (output string, ok bool)
	// Images attaches pictures to the opening task (-image with
	// -tui); follow-up turns attach their own @paths per turn.
	Images []string
	// AuditPath, when set, appends gate decisions as JSONL (see audit).
	AuditPath string
}
type runState int

const (
	stRunning runState = iota
	stPermission
	stAsk
	stDone
	stStopping
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
	hist      []string
	histIdx   int
	queued    string
	gateDraft string
	draft     string
	events    chan api.Event
	hub       *api.GateHub
	cancel    context.CancelFunc
	ctx       context.Context
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
	maxSteps     int
	started      time.Time
	elapsed      time.Duration
	spinnerFrame int
	ready        bool
	answer       string
	runErr       error
	styles       styles
	quit         bool
	// interrupted marks a run stopped by esc rather than completion;
	// its doneMsg renders neutrally instead of as an error.
	interrupted bool
	// stateFile locates the saved transcript for follow-ups.
	stateFile string
	// ws resolves @image paths against the workspace, escape-checked
	// like every file tool path. Stored, not rebuilt per turn.
	ws *workspace.Workspace
	// newSession builds a session for a fresh task (first run and
	// /new). Follow-ups reuse the running session via Resume.
	newSession func(task, stateFile string, images []string) (*api.Session, error)
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
	// observed changed paths. Both reset in startRun with steps and
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
	alwaysMu     sync.Mutex
	always       map[[2]string]bool
	// dialog is the open modal, if any (dialog.go). While open it
	// replaces the transcript body and eats all keys but close/quit.
	dialog *dialog
	// sessions is the open sessions screen, if any (sessions.go): a
	// browsable past-task list with rename/delete. Like a dialog it
	// replaces the body and owns its keys; unlike a dialog it borrows
	// the note input for rename entry.
	sessions      *sessionsState
	nav           sessionsState
	navFocused    bool
	sidebarHidden bool
	drafts        map[string]chatDraft
	plan          bool
	mission       bool
	gateVP        viewport.Model
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
	env.OnResult = func(agent.ToolResult) {}
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
	uiCtx, cancelUI := context.WithCancel(ctx)
	defer cancelUI()
	eventsCh := make(chan api.Event, 256)
	hub := api.NewGateHub(func(name string, fields map[string]any) {
		select {
		case eventsCh <- gateEvent(name, fields):
		case <-uiCtx.Done():
		}
	})
	runCtx, cancel := context.WithCancel(uiCtx)
	m := &model{
		events:    eventsCh,
		hub:       hub,
		cancel:    cancel,
		ctx:       runCtx,
		base:      uiCtx,
		started:   time.Now(),
		styles:    defaultStyles(),
		limit:     cfg.Env.ContextLimit,
		maxSteps:  envMaxSteps(cfg.Env),
		follow:    true,
		compact:   true,
		plan:      cfg.Plan,
		mission:   cfg.Mission != nil,
		stateFile: cfg.StateFile,
		note:      newNote(),
		always:    map[[2]string]bool{},
		// Status facts come from cfg once: the TUI never re-reads Env.
		backendKind: cfg.Env.BackendKind,
		modelName:   cfg.Env.Model,
		ws:          cfg.Env.WS,
		wsRoot:      wsRootOf(cfg.Env.WS),
		thinkBudget: cfg.Env.ReasoningBudget,
		mcpCount:    len(cfg.Env.MCPTools),
		policyRules: len(cfg.Env.Policy.Rules),
	}
	if len(cfg.History) > 0 && !cfg.Plan && readChatMode(filepath.Dir(cfg.StateFile)) == "Plan" {
		m.plan = true
	}
	m.refreshNavigator()
	m.input = newInput()
	env := withoutPrintHooks(cfg.Env)
	env.Gate = nil
	env.GateContext = audit.ContextHook(cfg.AuditPath, "tui", m.contextGate(hub, cfg.Env.WS))
	m.newSession = func(task, stateFile string, images []string) (*api.Session, error) {
		return api.New(api.Config{
			Env:          env,
			Task:         task,
			Plan:         m.plan,
			StateFile:    stateFile,
			Verify:       cfg.Verify,
			Images:       images,
			Answer:       hub.Suspender(),
			ResumeAnswer: cfg.ResumeAnswer,
			OnEvent: func(ev api.Event) {
				select {
				case eventsCh <- ev:
				case <-uiCtx.Done():
				}
			},
		})
	}
	inputOptions, restoreInput, err := consoleInput(uiCtx)
	if err != nil {
		return "", err
	}
	defer restoreInput()
	prog := tea.NewProgram(m, append(inputOptions, tea.WithContext(uiCtx))...)
	m.send = func(msg tea.Msg) {
		if done, ok := msg.(doneMsg); ok {
			select {
			case eventsCh <- api.Event{Name: "_done", Fields: map[string]any{"answer": done.answer, "error": done.err}}:
			case <-uiCtx.Done():
			}
			return
		}
		prog.Send(msg)
	}
	sendEvent := func(ev api.Event) {
		select {
		case eventsCh <- ev:
		case <-uiCtx.Done():
		}
	}
	switch {
	case cfg.Mission != nil:
		m.appendBlock(userBlock(cfg.Task))
		m.startRun(func(runCtx context.Context) (string, error) {
			report := ""
			emitter := events.New(api.EventWriter(func(ev api.Event) {
				if ev.Name == "result" {
					report, _ = ev.Fields["report"].(string)
				}
				sendEvent(ev)
			}))
			missionEnv := env
			missionEnv.OnNudge = func(kind, text string) { api.EmitNudge(emitter, kind, text) }
			err := cfg.Mission(runCtx, missionEnv, hub.Suspender(), emitter)
			return report, err
		})
	case len(cfg.History) > 0:
		task := cfg.Task
		if strings.TrimSpace(task) == "" {
			task = "Continue the saved task."
		}
		sess, err := m.newSession(task, m.stateFile, cfg.Images)
		if err != nil {
			return "", err
		}
		m.sess = sess
		m.blocks = append(m.blocks, renderHistory(cfg.History)...)
		m.startRun(func(runCtx context.Context) (string, error) {
			return sess.Resume(runCtx, cfg.History, task, cfg.Images...)
		})
	case strings.TrimSpace(cfg.Task) != "":
		if err := m.startInitial(cfg.Task, cfg.Images); err != nil {
			return "", err
		}
	default:
		m.state = stDone
		m.stateFile = ""
		m.appendBlock(markerBlock("New chat · type a task to begin\n/mode plan previews changes · /mode act executes them\n@path attaches images"))
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
		return "", nil
	}
	return fm.answer, fm.runErr
}
