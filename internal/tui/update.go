package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/keshon/tars/internal/agent"
	"github.com/keshon/tars/internal/api"
)

func (m *model) Init() tea.Cmd {
	// The terminal owns the native caret's blink and shape.
	return tea.Batch(waitEvents(m), tick(m.busy()))
}
func waitEvents(m *model) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-m.events
		if !ok {
			return nil
		}
		if ev.Name == "_done" {
			answer, _ := ev.Fields["answer"].(string)
			err, _ := ev.Fields["error"].(error)
			return doneMsg{answer: answer, err: err}
		}
		return eventMsg(ev)
	}
}
func tick(active bool) tea.Cmd {
	interval := time.Second
	if active {
		interval = 100 * time.Millisecond
	}
	return tea.Tick(interval, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	defer m.syncSuggestions()
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
		if m.busy() {
			m.spinnerFrame = (m.spinnerFrame + 1) % len(activityFrames)
		} else {
			m.spinnerFrame = 0
		}
		return m, tick(m.busy())
	case eventMsg:
		if m.state != stStopping {
			m.handleEvent(api.Event(msg))
		}
		return m, waitEvents(m)
	case doneMsg:
		m.persistUsage()
		wasInterrupted := m.interrupted
		m.answer = msg.answer
		m.runErr = msg.err
		m.state = stDone
		m.refreshNavigator()
		// Partial live text is not an answer: discard it either way.
		m.live, m.liveCut, m.livePainted = "", false, 0
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
		m.alwaysMu.Lock()
		m.always = map[[2]string]bool{}
		m.alwaysMu.Unlock()
		if m.gateDraft != "" {
			m.input.SetValue(m.gateDraft)
			m.gateDraft = ""
		}
		m.fitInput()
		m.fitBottom()
		if m.queued != "" {
			queued := m.queued
			if m.runErr == nil && !wasInterrupted && strings.TrimSpace(m.input.Value()) == "" {
				m.queued = ""
				m.input.SetValue(queued)
				_, cmd := m.followUp()
				return m, tea.Batch(cmd, waitEvents(m))
			}
		}
		return m, tea.Batch(m.input.Focus(), waitEvents(m))
	case tea.MouseMsg:
		if handled, cmd := m.headerMouse(msg); handled {
			return m, cmd
		}
		if m.actionBarVisible() && msg.Mouse().Y >= m.termH-m.actionBarRows() && msg.Mouse().Y < m.termH && msg.Mouse().Button == tea.MouseLeft && isMouseClick(msg) {
			for _, cell := range m.footerCells() {
				if msg.Mouse().Y == m.termH-m.actionBarRows()+cell.row && msg.Mouse().X >= cell.start && msg.Mouse().X < cell.end {
					return m.footerKey(tea.KeyPressMsg{Code: cell.action.key})
				}
			}
			return m, nil
		}
		if m.state == stPermission || m.state == stAsk {
			if msg.Mouse().X < m.conversationOffset() || msg.Mouse().Y < 3 || msg.Mouse().Y >= 3+m.vp.Height() {
				return m, nil
			}
			var cmd tea.Cmd
			m.gateVP, cmd = m.gateVP.Update(msg)
			return m, cmd
		}
		if m.sessions != nil {
			return m.browserMouse(msg)
		}
		if m.sidebarVisible() && msg.Mouse().X < navigatorWidth && msg.Mouse().Y >= 3 && msg.Mouse().Y < 3+m.paneHeight() {
			if m.state == stPermission || m.state == stAsk {
				return m, nil
			}
			switch msg.Mouse().Button {
			case tea.MouseWheelUp:
				m.nav.cursor--
			case tea.MouseWheelDown:
				m.nav.cursor++
			case tea.MouseLeft:
				if !isMouseClick(msg) {
					return m, nil
				}
				row := msg.Mouse().Y - 3
				if msg.Mouse().Y < 3 || row >= m.navigatorRows() || m.nav.offset+row >= len(m.nav.entries) {
					return m, nil
				}
				m.nav.cursor = m.nav.offset + row
			default:
				return m, nil
			}
			m.nav.clamp(m.navigatorRows())
			m.navFocused = true
			m.input.Blur()
			return m, nil
		}
		if m.dialog == nil && m.sessions == nil && m.sidebarVisible() && msg.Mouse().X >= navigatorWidth+3 && msg.Mouse().Y >= 3 && msg.Mouse().Y < 3+m.paneHeight() && msg.Mouse().Button == tea.MouseLeft && isMouseClick(msg) {
			m.navFocused = false
			return m, m.input.Focus()
		}
		if m.dialog != nil && m.dialog.help {
			switch msg.Mouse().Button {
			case tea.MouseWheelDown:
				m.dialog.offset++
			case tea.MouseWheelUp:
				m.dialog.offset = max(m.dialog.offset-1, 0)
			}
			return m, nil
		}
		// An overlay freezes the background: scroll resumes on close.
		if m.dialog != nil || m.sessions != nil {
			return m, nil
		}
		// The input never consumes mouse messages, so scroll works in
		// every state: wheel in ask/done used to fall through and die.
		return m.scrollViewport(msg)
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	default:
		// Clipboard and other widget messages reach the focused input.
		// Background panes never consume the active composer's input.
		if m.state == stPermission && m.gstage == gsReject {
			var cmd tea.Cmd
			m.note, cmd = m.note.Update(msg)
			return m, cmd
		}
		if m.sessions != nil && m.sessions.mode == sessRename {
			var cmd tea.Cmd
			m.note, cmd = m.note.Update(msg)
			return m, cmd
		}
		if m.sessions != nil && m.sessions.mode == sessList {
			var cmd tea.Cmd
			m.sessions.filter, cmd = m.sessions.filter.Update(msg)
			return m, cmd
		}
		if m.input.Focused() && !m.headerFocused {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			m.fitInput()
			m.fitBottom()
			return m, cmd
		}
	}
	return m, nil
}
func (m *model) width(msg tea.WindowSizeMsg) {
	m.termW, m.termH = msg.Width, msg.Height
	if !m.ready {
		m.vp = viewport.New(viewport.WithWidth(msg.Width), viewport.WithHeight(msg.Height))
		m.ready = true
	} else {
		m.vp.SetWidth(msg.Width)
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
	m.input.SetWidth(max(m.vp.Width()-5, 1))
	m.note.SetWidth(max(m.vp.Width()-10, 1))
	if m.sessions != nil {
		m.sessions.filter.SetWidth(max(msg.Width-14, 1))
	}
	// Height follows the terminal too: without a refit here a resize
	// leaves a stale viewport height, clipping content by the delta.
	m.fitBottom()
	// Render (or re-render): blocks appended before the first resize
	// never reached the viewport, and reflow follows later resizes.
	m.refreshContent()
}

// fitInput sizes the answer box to its content: one row for short
// answers, growing to MaxHeight, then scrolling internally.
func (m *model) fitInput() {
	if m.ready {
		// DynamicHeight counts wrapped rows and clamps the scroll offset when
		// the composer grows, keeping earlier lines visible after a newline.
		m.input.SetWidth(max(m.termW-m.conversationOffset()-5, 1))
	}
}

// fitBottom resizes the viewport so transcript + status + bottom bar
// always fit the terminal exactly. Called on every state transition
// and every input keystroke, since the answer box grows as you type.
func (m *model) fitBottom() {
	if !m.ready {
		return
	}
	m.input.SetWidth(max(m.termW-m.conversationOffset()-5, 1))
	// Done budgets the input box only (the hint moved right into the
	// status row); a run error adds its own line on top.
	lines := 1
	if m.state == stDone && m.runErr != nil {
		lines++
	}
	// The reject stage shows label + input: two bottom lines.
	if m.state == stPermission && m.gstage == gsReject {
		lines = 2
	}
	if m.state == stAsk || m.state == stDone || m.state == stRunning || m.state == stStopping {
		lines += m.input.Height() - 1
	}
	// The -7 counts header, identity, header rule, status, divider,
	// bottom base, and the breathing blank line above the divider
	// (layout, not content: a content trailing newline breaks scroll
	// math — phantom blank rows mid-scroll).
	// `lines` adds per-state extras (done hint, reject label, input growth).
	// A takeover overlay owns the bottom bar (one slim line), so it
	// budgets nothing extra: the chat input hides with it.
	if m.queued != "" && m.state != stPermission && m.state != stAsk {
		lines++
	}
	if m.dialog != nil {
		lines = 1
	}
	oldWidth := m.vp.Width()
	m.fitColumns()
	m.note.SetWidth(max(m.vp.Width()-10, 1))
	if oldWidth != m.vp.Width() {
		m.refreshContent()
	}
	h := m.termH - 6 - lines
	if m.actionBarVisible() {
		h -= m.footerRows()
	}
	if h < 1 {
		h = 1
	}
	m.vp.SetHeight(h)
	m.gateVP.SetWidth(m.vp.Width())
	m.gateVP.SetHeight(h)
	if m.state == stPermission || m.state == stAsk {
		m.refreshGate()
	}
	if m.sessions != nil {
		width := m.searchGeometry().width - 2
		m.sessions.filter.SetWidth(max(width-lipgloss.Width(m.sessions.filter.Prompt)-1, 1))
		if m.sessions.mode == sessRename {
			m.note.SetWidth(max(width-lipgloss.Width(m.note.Prompt)-1, 1))
		}
	}
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
		// The enforced step budget rides the event (base plus any
		// todo-funded extension): the meter reads the enforced line,
		// never a second literal. Absent on old producers — keep ours.
		if ms, ok := ev.Fields["max_steps"].(float64); ok && ms > 0 {
			m.maxSteps = int(ms)
		}
		// The live buffer belongs to this response: clear it first so
		// the authoritative blocks below never duplicate it. A step
		// with empty text but a non-empty buffer adopts the buffer
		// (backend streamed text it then omitted); tool steps never
		// adopt, calls are work.
		live := m.live
		m.live, m.liveCut, m.livePainted = "", false, 0
		// Reasoning above its reply (TUI-15, user call): the collapsed
		// think line introduces the answer it produced, causal order.
		// Tool calls render as cards below, whatever prompted them.
		reasoning, _ := ev.Fields["reasoning"].(string)
		text, _ := ev.Fields["text"].(string)
		if text == "" && live != "" {
			if calls, _ := ev.Fields["tool_calls"].([]any); len(calls) == 0 {
				text = live
			}
		}
		for _, b := range assistantBlocks(text, reasoning) {
			m.appendBlock(b)
		}

		if calls, _ := ev.Fields["tool_calls"].([]any); len(calls) > 0 {
			for _, c := range calls {
				call, _ := c.(map[string]any)
				name, _ := call["name"].(string)
				args, _ := call["args"].(string)
				id, _ := call["id"].(string)
				m.toolsUsed++
				m.appendBlock(toolCardBlock(id, name+" "+truncate(args, 120)))
			}
		}
	case "tool_result":
		text, _ := ev.Fields["text"].(string)
		callID, _ := ev.Fields["call_id"].(string)
		var status []bool
		if failed, ok := ev.Fields["failed"].(bool); ok {
			status = []bool{failed}
		}
		if !m.attachResult(callID, text, status...) {
			m.appendBlock(resultBlock(text))
		}
	case "finding":
		rule, _ := ev.Fields["rule"].(string)
		path, _ := ev.Fields["path"].(string)
		line, _ := ev.Fields["line"].(float64)
		summary, _ := ev.Fields["summary"].(string)
		if rule != "" {
			m.appendBlock(findingBlock(rule, path, int(line), summary))
		}
	case "nudge":
		// Harness notices render as dim markers: same text the model
		// saw (provenance mark included), no decisions attached.
		if text, _ := ev.Fields["text"].(string); text != "" {
			m.appendBlock(markerBlock(text))
		}
	case "usage":
		if p, ok := ev.Fields["prompt"].(float64); ok && int(p) > 0 {
			m.tokens = int(p)
			m.tokensEst, _ = ev.Fields["estimated"].(bool)
			m.contextSource = "Last request prompt"
			if m.tokensEst {
				m.contextSource = "Last request estimate"
			}
			m.usageAt = time.Now()
			m.usageThisRun = true
			m.lastUsage = usageSnapshot{Tokens: m.tokens, Estimated: m.tokensEst, At: m.usageAt, Model: m.modelName}
		}
	case "awaiting_input":
		m.closeHeader()
		kind, _ := ev.Fields["kind"].(string)
		prompt, _ := ev.Fields["prompt"].(string)
		m.gate = prompt
		m.gateVP = viewport.New(viewport.WithWidth(max(m.termW, 1)), viewport.WithHeight(max(m.vp.Height(), 1)))
		if m.sessions != nil {
			m.closeSessions()
		}
		if m.dialog != nil {
			m.closeDialog()
		}
		m.navFocused = false
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
			m.gateDraft = m.input.Value()
			m.input.SetValue("")
			m.input.Focus()
		}
		m.fitInput()
		m.fitBottom()
	case "input_answered":
		m.state = stRunning
		m.gate = ""
		m.input.SetValue(m.gateDraft)
		m.gateDraft = ""
		m.input.Focus()
		m.fitInput()
		m.fitBottom()
	case "result":
		if report, ok := ev.Fields["report"].(string); ok && report != "" {
			m.appendBlock(answerBlock(report))
		}
	case "outcome":
		m.filesTouched = map[string]bool{}
		outcome, _ := ev.Fields["outcome"].(string)
		var files []string
		if values, ok := ev.Fields["files"].([]any); ok {
			for _, value := range values {
				if path, ok := value.(string); ok {
					files = append(files, path)
					m.filesTouched[path] = true
				}
			}
		}
		checks, _ := ev.Fields["verification"].(string)
		if checks == "" {
			checks = "no current command verdict"
		}
		resume, _ := ev.Fields["resume"].(string)
		text := fmt.Sprintf("outcome: %s\nfiles: %s\nchecks: %s", outcome, strings.Join(files, ", "), truncate(checks, 512))
		if resume != "" {
			text += "\nresume: agent -tui -resume " + resume
		}
		m.appendBlock(markerBlock(text))
	case "mission":
		if text, _ := ev.Fields["text"].(string); text != "" {
			m.appendBlock(missionBlock("[mission] " + text))
		}
	case "delta":
		// Streamed chunk: accumulate into the live buffer, repaint on
		// completed lines or heartbeat (spec P26). No markdown yet
		// (partial spans would break); the step event brings the
		// full render.
		if text, _ := ev.Fields["text"].(string); text != "" && !m.liveCut {
			if m.firstToken.IsZero() {
				m.firstToken = time.Now()
			}
			combined := m.live + text
			if r := []rune(combined); len(r) > liveMaxRunes {
				combined = string(r[:liveMaxRunes]) + "\n…(live truncated)"
				m.liveCut = true
			}
			m.live = combined
			if livePaintDue(m) {
				m.livePainted = len(m.live)
				m.lastLive = time.Now()
				m.refreshContent()
			}
		}
	}
}
func (m *model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.headerFocused {
		return m.headerKey(msg)
	}
	switch msg.String() {
	case "f1", "f2", "f3", "f4", "f5", "f6", "f7", "f8", "f9", "f10":
		return m.footerKey(msg)
	}
	if msg.String() == "ctrl+q" || msg.String() == "ctrl+c" {
		m.quit = true
		m.cancel()
		return m, tea.Quit
	}
	if m.state == stPermission || m.state == stAsk {
		if msg.String() == "pgup" || msg.String() == "pgdown" || msg.String() == "ctrl+end" {
			var cmd tea.Cmd
			if msg.String() == "ctrl+end" {
				m.gateVP.GotoBottom()
				return m, nil
			}
			m.gateVP, cmd = m.gateVP.Update(msg)
			return m, cmd
		}
	}
	if m.sessions == nil && m.dialog == nil && m.state != stPermission {
		switch msg.String() {
		case "ctrl+end":
			m.vp.GotoBottom()
			m.follow = true
			return m, nil
		case "ctrl+u":
			m.input.SetValue("")
			m.fitInput()
			m.fitBottom()
			return m, nil
		case "ctrl+x":
			if m.queued != "" {
				m.queued = ""
				m.fitBottom()
				return m, nil
			}
		case "ctrl+e":
			if m.queued != "" && m.state != stAsk {
				if m.input.Value() != "" {
					m.appendBlock(markerBlock("Keep or clear the current draft before editing the queue."))
					return m, nil
				}
				m.input.SetValue(m.queued)
				m.queued = ""
				m.fitInput()
				m.fitBottom()
				return m, m.input.Focus()
			}
		}
	}
	m.syncSuggestions()
	commandSelected := len(m.suggestions.items) > 0 && strings.HasPrefix(m.suggestions.items[m.suggestions.selected].value, "/")
	if m.suggestionKey(msg) {
		if msg.String() != "enter" || !commandSelected {
			return m, nil
		}
		if m.state == stDone {
			return m.followUp()
		}
		// During a run, queue the completed command through the normal Enter path.
	}
	if m.sessions == nil && m.dialog == nil && !m.navFocused && (m.state == stDone || m.state == stAsk || m.state == stRunning) && msg.Mod.Contains(tea.ModAlt) && (msg.Code == tea.KeyUp || msg.Code == tea.KeyDown) {
		m.historyWalk(msg.Code == tea.KeyUp)
		m.fitInput()
		m.fitBottom()
		return m, nil
	}
	if m.state != stPermission && m.state != stAsk && m.sessions == nil && m.dialog == nil {
		switch msg.String() {
		case "ctrl+p":
			m.openSessions()
			return m, nil
		case "ctrl+b":
			m.sidebarHidden = !m.sidebarHidden
			m.fitBottom()
			m.refreshContent()
			return m, nil
		case "tab", "shift+tab":
			if m.sidebarVisible() {
				m.navFocused = !m.navFocused
				if m.navFocused {
					m.input.Blur()
				} else {
					return m, m.input.Focus()
				}
				return m, nil
			}
		case "ctrl+n":
			if m.state == stDone {
				m.navFocused = false
				m.newChat()
				return m, m.input.Focus()
			}
			return m, nil
		}
		if m.navFocused {
			return m.navigatorKey(msg)
		}
	}
	if m.sessions != nil && m.state != stPermission {
		// The sessions screen owns its keys like a dialog — except
		// over a permission gate, which is always on top: its y/n/a
		// answers must never land in a list.
		return m.sessionsKey(msg)
	}
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
		if m.dialog.help {
			switch msg.String() {
			case "tab", "right":
				m.dialog.page = (m.dialog.page + 1) % len(helpSections())
				m.dialog.offset = 0
			case "shift+tab", "left":
				m.dialog.page = (m.dialog.page + len(helpSections()) - 1) % len(helpSections())
				m.dialog.offset = 0
			case "down":
				m.dialog.offset++
			case "up":
				m.dialog.offset = max(m.dialog.offset-1, 0)
			case "pgdown":
				m.dialog.offset += max(m.vp.Height()/2, 1)
			case "pgup":
				m.dialog.offset = max(m.dialog.offset-max(m.vp.Height()/2, 1), 0)
			}
		}
		return m, nil
	}
	if msg.String() == "ctrl+g" {
		return m.footerKey(tea.KeyPressMsg{Code: tea.KeyF3})
	}

	switch m.state {
	case stPermission:
		return m.gateKey(msg)
	case stAsk:
		switch msg.String() {
		case "esc":
			if answer := m.input.Value(); answer != "" {
				if m.gateDraft != "" {
					answer = m.gateDraft + "\n" + answer
				}
				m.input.SetValue(answer)
				m.gateDraft = ""
				m.fitInput()
			}
			m.interrupted = true
			m.cancel()
			m.state = stStopping
			m.fitBottom()
			return m, nil
		case "pgup", "pgdown":
			// Paging scrolls the transcript in every state; the
			// input keeps arrows and typing only.
			return m.scrollViewport(msg)
		case "enter":
			m.pushHistory(strings.TrimSpace(m.input.Value()))
			_ = m.hub.Respond(m.input.Value())
			m.state = stRunning
			return m, nil
		case "ctrl+c", "ctrl+q":
			m.quit = true
			m.cancel()
			return m, tea.Quit

		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.fitInput()
		m.fitBottom()
		return m, cmd
	case stDone:
		switch msg.String() {
		case "pgup", "pgdown":
			// Same routing as ask: paging belongs to the transcript.
			return m.scrollViewport(msg)
		case "enter":
			return m.followUp()
		case "esc":
			return m, nil
		case "ctrl+c", "ctrl+q":
			// Quit lives on ctrl+q (and ctrl+c): a letter key must
			// never quit, or words starting with q ("queen") become
			// untypable on an empty box. Empty+enter still quits.
			m.quit = true
			m.cancel()
			return m, tea.Quit

		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.fitInput()
		m.fitBottom()
		return m, cmd
	default:
		switch msg.String() {
		case "ctrl+c", "ctrl+q":
			m.quit = true
			m.cancel()
			return m, tea.Quit
		case "esc":
			if m.state != stStopping {
				m.interrupted = true
				m.cancel()
				m.state = stStopping
				m.fitBottom()
			}
			return m, nil
		case "enter":
			if m.state == stStopping {
				return m, nil
			}
			text := strings.TrimSpace(m.input.Value())
			if text == "" {
				return m, nil
			}
			if m.queued != "" {
				m.appendBlock(markerBlock("a follow-up is already queued"))
				return m, nil
			}
			m.queued = text
			m.input.SetValue("")
			m.appendBlock(markerBlock("queued follow-up: " + truncate(text, 120)))
			m.fitInput()
			m.fitBottom()
			return m, nil
		case "pgup", "pgdown":
			return m.scrollViewport(msg)
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.fitInput()
		m.fitBottom()
		return m, cmd
	}
}

func isMouseClick(msg tea.MouseMsg) bool { _, ok := msg.(tea.MouseClickMsg); return ok }
