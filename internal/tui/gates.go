package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/keshon/tars/internal/agent"
	"github.com/keshon/tars/internal/api"
	"github.com/keshon/tars/internal/permission"
	"github.com/keshon/tars/internal/tools"
	"github.com/keshon/tars/internal/workspace"
)

// gateStage is the permission overlay stage (opencode permission.tsx
// steal: permit -> always-confirm -> reject-with-note). Ask gates keep
// their own path and never touch stages.
type gateStage int

const (
	// gsPermit answers inline: allow once, escalate to always-confirm,
	// or drop to reject-with-note.
	gsPermit gateStage = iota
	// gsAlways confirms the run-local always scope before answering.
	gsAlways
	// gsReject takes a redirect note; empty means plain deny.
	gsReject
)

// newNote builds the reject-note input: single line, capped, no
// history. Esc is handled by the stager (back), never the widget.
func newNote() textinput.Model {
	ti := textinput.New()
	ti.SetVirtualCursor(false)
	style := ti.Styles()
	style.Cursor.Shape = tea.CursorUnderline
	ti.SetStyles(style)
	ti.Prompt = "note> "
	ti.CharLimit = 240
	return ti
}

// gateKey handles keys while a permission gate is open, one stage at
// a time. It answers through resolveGate only: every exit path tears
// the overlay down, so a answered gate can never strand a stage.
func (m *model) gateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	defer m.fitBottom()
	if msg.String() == "ctrl+q" {
		// Quit works from every gate stage: letters are answers here.
		m.quit = true
		m.cancel()
		return m, tea.Quit
	}
	switch m.gstage {
	case gsAlways:
		switch msg.String() {
		case "enter":
			m.resolveGate("a")
			return m, nil
		case "esc":
			m.gstage = gsPermit
			return m, nil
		}
		return m, nil
	case gsReject:
		switch msg.String() {
		case "enter":
			note := strings.TrimSpace(m.note.Value())
			ans := "n"
			if note != "" {
				ans = "n: " + note
				rb := userBlock("reject: " + note)
				rb.breakBefore = false
				m.appendBlock(rb)
			}
			m.resolveGate(ans)
			return m, nil
		case "esc":
			m.note.Blur()
			m.note.SetValue("")
			m.gstage = gsPermit
			return m, nil
		case "ctrl+c":
			m.quit = true
			m.cancel()
			return m, tea.Quit
		}
		var cmd tea.Cmd
		m.note, cmd = m.note.Update(msg)
		return m, cmd
	default: // gsPermit
		switch msg.String() {
		case "y", "Y":
			m.resolveGate("y")
		case "a", "A":
			m.gstage = gsAlways
		case "n", "N":
			m.gstage = gsReject
			m.note.SetValue("")
			return m, m.note.Focus()
		case "esc":
			// Esc denies, as before: a gate left open would hang the
			// run, and there is no "later" to defer to.
			m.resolveGate("n")
		case "ctrl+c":
			m.quit = true
			m.cancel()
			return m, tea.Quit
		}
		return m, nil
	}
}

// resolveGate answers the pending gate and tears the overlay down:
// stage reset, note blurred, focus restored, run resumed.
func (m *model) resolveGate(answer string) {
	_ = m.hub.Respond(answer)
	m.gstage = gsPermit
	m.note.Blur()
	m.popOverlay()
	m.state = stRunning
	m.fitBottom()
	m.refreshContent()
	m.input.Focus()
}

// gateBar renders the bottom bar for the permission overlay by stage.
// The reject stage costs two lines (label + input); fitBottom budgets
// it, the transcript keeps scrolling above.
func (m *model) gateBar() string {
	switch m.gstage {
	case gsAlways:
		return m.styles.gate.Render(fmt.Sprintf(
			"Enter confirm  Esc back  allow %s on %q for this run",
			m.gateTool, truncate(m.gateResource, 80)))
	case gsReject:
		return m.styles.dim.Render("reject with note (empty = plain deny)  [Esc] back") +
			"\n" + m.note.View()
	default:
		return m.styles.gate.Render("Y allow once  N deny with note  A allow this run  Esc deny")
	}
}

// gateHook is the legacy adapter; production gates receive the active run context.
func (m *model) gateHook(hub *api.GateHub, runCtx context.Context, ws *workspace.Workspace) func(tool, resource string, args json.RawMessage) (permission.Effect, error) {
	return func(tool, resource string, args json.RawMessage) (permission.Effect, error) {
		return m.contextGate(hub, ws)(runCtx, tool, resource, args)
	}
}
func (m *model) contextGate(hub *api.GateHub, ws *workspace.Workspace) func(context.Context, string, string, json.RawMessage) (permission.Effect, error) {
	return func(runCtx context.Context, tool, resource string, args json.RawMessage) (permission.Effect, error) {
		key := [2]string{tool, resource}
		m.alwaysMu.Lock()
		allowed := m.always[key]
		m.alwaysMu.Unlock()
		if allowed {
			return permission.Allow, nil
		}
		prompt := fmt.Sprintf("[permission] %s on %q", tool, resource)
		if preview := tools.PreviewArgs(ws, tool, args); preview != "" {
			prompt += "\n" + preview
		}
		rep, err := hub.Suspender()(runCtx, agent.SuspendRequest{
			Kind:     agent.SuspendPermission,
			Tool:     tool,
			Resource: resource,
			Prompt:   prompt,
		})
		if err != nil {
			return permission.Deny, err
		}
		eff, derr := permission.Decide(rep.Answer)
		if derr != nil {
			return permission.Deny, derr
		}
		if isAlwaysAnswer(rep.Answer) {
			m.alwaysMu.Lock()
			m.always[key] = true
			m.alwaysMu.Unlock()
		}
		return eff, nil
	}
}

// isAlwaysAnswer reports whether the reply asks for run-long allow.
// Mirrors permission.Decide's allow list; kept beside the memory that
// depends on it.
func isAlwaysAnswer(answer string) bool {
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "a", "always":
		return true
	}
	return false
}
func (m *model) refreshGate() {
	title := "Permission required"
	if m.state == stAsk {
		title = "Answer required"
	}
	rows := []string{m.styles.gate.Render(title), ""}
	for _, line := range strings.Split(m.gate, "\n") {
		rows = append(rows, reflow(line, max(m.gateVP.Width()-1, 1))...)
	}
	m.gateVP.SetContent(strings.Join(rows, "\n"))
}
