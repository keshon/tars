package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/keshon/tars/internal/agent"
	"github.com/keshon/tars/internal/api"
)

func (m *model) Init() tea.Cmd {
	return tea.Batch(waitEvents(m), tick())
}

func waitEvents(m *model) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-m.events
		if !ok {
			return nil
		}
		return eventMsg(ev)
	}
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width(msg)
		return m, nil

	case tickMsg:
		// The clock runs only while a run is in flight (gates
		// included: a run blocked on input hasn't finished). Once
		// done, elapsed freezes instead of counting idle reading time.
		if m.state != stDone {
			m.elapsed = time.Since(m.started).Round(time.Second)
		}
		return m, tick()

	case eventMsg:
		m.handleEvent(api.Event(msg))
		return m, waitEvents(m)

	case doneMsg:
		m.answer = msg.answer
		m.runErr = msg.err
		m.state = stDone
		if m.interrupted {
			m.interrupted = false
			m.runErr = nil
			m.appendBlock(markerBlock("— interrupted —"))
		} else {
			m.appendBlock(markerBlock("— run finished —"))
			if msg.err != nil {
				// Failures live in the transcript, not just the bottom
				// bar: scrolled-up context keeps the error beside the
				// turn that produced it. /retry re-runs this turn.
				m.appendBlock(errorBlock("failed: " + msg.err.Error() + "\n/retry re-runs this turn"))
			}
		}
		m.input.SetValue("")
		m.fitBottom()
		return m, m.input.Focus()

	case tea.MouseMsg:
		// A dialog freezes the background: scroll resumes on close.
		if m.dialog != nil {
			return m, nil
		}
		// The input never consumes mouse messages, so scroll works in
		// every state: wheel in ask/done used to fall through and die.
		return m.scrollViewport(msg)

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *model) width(msg tea.WindowSizeMsg) {
	m.termW, m.termH = msg.Width, msg.Height
	if !m.ready {
		m.vp = viewport.New(msg.Width, msg.Height)
		m.ready = true
	} else {
		m.vp.Width = msg.Width
	}
	// Cap the answer box like a chat input, not a fullscreen editor.
	maxRows := msg.Height / 4
	if maxRows < 2 {
		maxRows = 2
	}
	if maxRows > 6 {
		maxRows = 6
	}
	m.input.MaxHeight = maxRows
	m.fitInput()
	m.fitBottom()
	m.input.SetWidth(msg.Width - 4)
	m.note.Width = max(msg.Width-10, 10)
	// Render (or re-render): blocks appended before the first resize
	// never reached the viewport, and reflow follows later resizes.
	m.refreshContent()
}

// fitInput sizes the answer box to its content: one row for short
// answers, growing to MaxHeight, then scrolling internally.
func (m *model) fitInput() {
	m.input.SetHeight(min(max(m.input.LineCount(), 1), max(m.input.MaxHeight, 1)))
}

// fitBottom resizes the viewport so transcript + status + bottom bar
// always fit the terminal exactly. Called on every state transition
// and every input keystroke, since the answer box grows as you type.
func (m *model) fitBottom() {
	if !m.ready {
		return
	}
	lines := 1
	if m.state == stDone {
		lines = 2
	}
	// The reject stage shows label + input: two bottom lines.
	if m.state == stPermission && m.gstage == gsReject {
		lines = 2
	}
	if m.state == stAsk || m.state == stDone {
		lines += m.input.Height() - 1
	}
	// The -3 counts status line, divider rule, and the bottom base;
	// `lines` adds per-state extras (done hint, reject label, input growth).
	h := m.termH - 3 - lines
	if h < 1 {
		h = 1
	}
	m.vp.Height = h
}

// scrollViewport forwards a scroll message (mouse or paging key) to
// the transcript and refreshes follow stickiness. Every state routes
// through it, so scroll never depends on which widget is focused.
func (m *model) scrollViewport(msg tea.Msg) (tea.Model, tea.Cmd) {
	if !m.ready {
		return m, nil
	}
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	m.follow = m.vp.AtBottom()
	return m, cmd
}

func (m *model) handleEvent(ev api.Event) {
	switch ev.Name {
	case "step":
		m.steps++
		// Answer first, reasoning under it: the collapsed think line
		// reads as a footnote to the response above (chat convention,
		// newest-at-bottom). TUI-15 decided think-after-answer.
		if text, _ := ev.Fields["text"].(string); text != "" {
			m.appendBlock(answerBlock(text))
		}
		if reasoning, _ := ev.Fields["reasoning"].(string); reasoning != "" {
			m.appendBlock(thinkBlock(reasoning))
		}
		if calls, _ := ev.Fields["tool_calls"].([]any); len(calls) > 0 {
			for _, c := range calls {
				call, _ := c.(map[string]any)
				name, _ := call["name"].(string)
				args, _ := call["args"].(string)
				id, _ := call["id"].(string)
				m.appendBlock(toolCardBlock(id, name+" "+truncate(args, 120)))
			}
		}
	case "tool_result":
		text, _ := ev.Fields["text"].(string)
		callID, _ := ev.Fields["call_id"].(string)
		if !m.attachResult(callID, text) {
			m.appendBlock(resultBlock(text))
		}
	case "usage":
		if p, ok := ev.Fields["prompt"].(float64); ok && int(p) > 0 {
			m.tokens = int(p)
		}
	case "awaiting_input":
		kind, _ := ev.Fields["kind"].(string)
		prompt, _ := ev.Fields["prompt"].(string)
		m.gate = prompt
		// The prompt lives in the transcript, not the bottom bar: the
		// bar has a fixed 1-line budget (2 with the answer box), so the
		// viewport math below always fits the terminal. A long question
		// stays readable by scrolling instead of pushing layout around.
		if prompt != "" {
			m.appendBlock(gateBlock(prompt))
		}
		if kind == string(agent.SuspendPermission) {
			m.state = stPermission
			m.gstage = gsPermit
			m.gateTool, _ = ev.Fields["tool"].(string)
			m.gateResource, _ = ev.Fields["resource"].(string)
			m.note.Blur()
			m.note.SetValue("")
			m.pushOverlay(ovGate)
		} else {
			m.state = stAsk
			m.input.SetValue("")
			m.input.Focus()
		}
	case "input_answered":
		m.state = stRunning
		m.gate = ""
		m.input.Blur()
		m.input.SetValue("")
	case "mission":
		if text, _ := ev.Fields["text"].(string); text != "" {
			m.appendBlock(missionBlock("[mission] " + text))
		}
	}
}

func (m *model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.dialog != nil {
		// Takeover: the dialog eats every key but close and quit so
		// typing can neither reach the input nor toggle state behind it.
		// Close is esc/enter only: "q" must stay typable for dialogs
		// with inputs tomorrow.
		switch msg.String() {
		case "esc", "enter":
			m.closeDialog()
			return m, nil
		case "ctrl+c":
			m.quit = true
			m.cancel()
			return m, tea.Quit
		}
		return m, nil
	}
	if msg.Type == tea.KeyCtrlG {
		// Global: thinking expand/collapse works in every state and
		// never reaches the input (the textarea binds no ctrl+g).
		m.showThink = !m.showThink
		m.refreshContent()
		return m, nil
	}
	switch m.state {
	case stPermission:
		return m.gateKey(msg)
	case stAsk:
		switch msg.Type {
		case tea.KeyPgUp, tea.KeyPgDown:
			// Paging scrolls the transcript in every state; the
			// input keeps arrows and typing only.
			return m.scrollViewport(msg)
		case tea.KeyEnter:
			m.pushHistory(strings.TrimSpace(m.input.Value()))
			_ = m.hub.Respond(m.input.Value())
			m.state = stRunning
			return m, nil
		case tea.KeyCtrlC, tea.KeyCtrlQ:
			m.quit = true
			m.cancel()
			return m, tea.Quit
		case tea.KeyUp:
			if m.historyWalk(true) {
				m.fitInput()
				m.fitBottom()
				return m, nil
			}
		case tea.KeyDown:
			if m.historyWalk(false) {
				m.fitInput()
				m.fitBottom()
				return m, nil
			}
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.fitInput()
		m.fitBottom()
		return m, cmd
	case stDone:
		switch msg.Type {
		case tea.KeyPgUp, tea.KeyPgDown:
			// Same routing as ask: paging belongs to the transcript.
			return m.scrollViewport(msg)
		case tea.KeyEnter:
			return m.followUp()
		case tea.KeyCtrlC, tea.KeyEsc, tea.KeyCtrlQ:
			// Quit lives on ctrl+q (and ctrl+c): a letter key must
			// never quit, or words starting with q ("queen") become
			// untypable on an empty box. Empty+enter still quits.
			m.quit = true
			m.cancel()
			return m, tea.Quit
		case tea.KeyUp:
			if m.historyWalk(true) {
				m.fitInput()
				m.fitBottom()
				return m, nil
			}
		case tea.KeyDown:
			if m.historyWalk(false) {
				m.fitInput()
				m.fitBottom()
				return m, nil
			}
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.fitInput()
		m.fitBottom()
		return m, cmd
	default:
		switch msg.String() {
		case "ctrl+c":
			m.quit = true
			m.cancel()
			return m, tea.Quit
		case "q", "esc":
			// Stop the run but stay in chat (TUI-21): while running
			// the input is blurred, so neither key is typing. Quitting
			// mid-run lives on ctrl+c only.
			m.interrupted = true
			m.cancel()
			m.state = stDone
			m.fitBottom()
			m.input.SetValue("")
			return m, m.input.Focus()
		case "end":
			// The viewport's keymap covers paging, not jump-to-end:
			// do it explicitly and re-arm follow directly.
			m.vp.GotoBottom()
			m.follow = true
		case "home":
			m.vp.GotoTop()
			m.follow = false
		}
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		// Stickiness follows the viewport, not the key: any scroll
		// away from the bottom (keys or wheel) unfollows; reaching the
		// bottom re-arms. New blocks never yank a reading user.
		m.follow = m.vp.AtBottom()
		return m, cmd
	}
}
