package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type headerState struct {
	focused bool
	index   int
	menu    string
	row     int
}

type headerBadge struct {
	id, label  string
	start, end int
}
type headerItem struct {
	text, command string
	enabled       bool
}

func (m *model) headerBadges() []headerBadge {
	mode, activity := "ACT", "READY"
	if m.plan {
		mode = "PLAN"
	}
	switch m.state {
	case stRunning:
		activity = string(activityFrames[m.spinnerFrame%len(activityFrames)]) + " WORK"
	case stPermission, stAsk:
		activity = "WAIT"
	case stStopping:
		activity = "STOP"
	default:
		if m.runErr != nil {
			activity = "ERROR"
		}
	}
	ctx := "CTX —"
	if m.tokens > 0 {
		ctx = "CTX " + kTokens(m.tokens)
		if m.tokensEst {
			ctx = "~" + ctx
		}
	}
	if m.limit > 0 {
		ctx += " / " + kTokens(m.limit)
	}
	modelWidth := 32
	if m.termW < 110 {
		modelWidth = 18
	}
	candidates := []headerBadge{{id: "mode", label: mode}, {id: "activity", label: activity}, {id: "model", label: strings.TrimSpace(cellLine(strings.ToUpper(nonEmpty(m.modelName)), modelWidth))}, {id: "context", label: ctx}}
	if m.steps > 0 || m.state != stDone {
		candidates = append(candidates, headerBadge{id: "run", label: fmt.Sprintf("step %d/%d  %s", m.steps, m.maxSteps, formatElapsed(m.elapsed))})
	}
	// Context and state stay visible before the optional model and run details.
	total := 0
	for _, b := range candidates {
		total += lipgloss.Width(b.label) + 3
	}
	if total > m.termW && len(candidates) > 4 {
		candidates = candidates[:4]
		total = 0
		for _, b := range candidates {
			total += lipgloss.Width(b.label) + 3
		}
	}
	if total > m.termW {
		candidates = append(candidates[:2], candidates[3:]...)
	}
	x := 0
	for i := range candidates {
		candidates[i].start = x
		candidates[i].end = min(x+lipgloss.Width(candidates[i].label)+2, m.termW)
		x = candidates[i].end + 1
	}
	return candidates
}

func (m *model) badgeLine() string {
	normal, _, selected := popupStyles()
	badges := m.headerBadges()
	index := min(max(m.header.index, 0), len(badges)-1)
	var out []string
	for i, b := range badges {
		style := normal
		switch b.id {
		case "mode":
			style = style.Foreground(lipgloss.Color(colorMode))
		case "activity":
			color := colorReady
			if m.state != stDone {
				color = colorBusy
			}
			if m.runErr != nil {
				color = colorFailed
			}
			style = style.Foreground(lipgloss.Color(color))
		}
		if m.header.focused && i == index {
			style = selected
		}
		out = append(out, style.Render(" "+b.label+" "))
	}
	return cellLine(strings.Join(out, " "), m.termW)
}

func (m *model) rawHeaderItems() []headerItem {
	info := func(s string) headerItem { return headerItem{text: s} }
	idle := m.state == stDone
	switch m.header.menu {
	case "mode":
		return []headerItem{{"ACT   Execute changes with tools", "/mode act", idle && !m.mission}, {"PLAN  Read-only exploration and planning", "/mode plan", idle && !m.mission}}
	case "activity":
		rows := []headerItem{info("State: " + m.statusWord()), info(fmt.Sprintf("Steps: %d / %d", m.steps, m.maxSteps)), info("Elapsed: " + formatElapsed(m.elapsed))}
		if m.runErr != nil {
			rows = append(rows, info("Error: "+m.runErr.Error()), headerItem{"Retry failed turn", "/retry", idle && m.retryRun != nil})
		}
		if m.busy() {
			rows = append(rows, headerItem{"Stop run", "stop", m.state == stRunning})
		}
		return rows
	case "model":
		transport := "Request / response"
		if m.streaming {
			transport = "Streaming"
		}
		return []headerItem{info("Model: " + nonEmpty(m.modelName)), info("Backend: " + nonEmpty(m.backendKind)), info("Endpoint: " + nonEmpty(m.backendURL)), info("Transport: " + transport), info(fmt.Sprintf("Context window: %d tokens", m.limit)), info(fmt.Sprintf("Response budget: %d tokens", m.responseCap))}
	case "context":
		rows := []headerItem{info("Context: " + strings.Replace(m.meter(), "ctx ", "", 1)), info("Source: " + nonEmpty(m.usage.source))}
		if !m.usage.at.IsZero() {
			rows = append(rows, info("Measured: "+m.usage.at.Format("15:04:05")))
		}
		if m.usage.last.Tokens > 0 {
			kind := "reported"
			if m.usage.last.Estimated {
				kind = "estimated"
			}
			rows = append(rows, info(fmt.Sprintf("Last request: %s tokens (%s)", kTokens(m.usage.last.Tokens), kind)), info("Request time: "+m.usage.last.At.Format("Jan 2 15:04:05")))
		}
		if m.usage.source == "Saved history estimate" {
			rows = append(rows, info("Estimate excludes tool schemas and images."))
		}
		return append(rows, headerItem{"Compact saved history", "/compact", idle && !m.mission && m.stateFile != ""})
	case "run":
		rows := []headerItem{info(fmt.Sprintf("Steps: %d / %d", m.steps, m.maxSteps)), info("Elapsed: " + formatElapsed(m.elapsed)), info(fmt.Sprintf("Tools: %d   Files: %d", m.toolsUsed, len(m.filesTouched)))}
		if !m.firstToken.IsZero() {
			rows = append(rows, info("First token: "+formatElapsed(m.firstToken.Sub(m.started))))
		}
		return rows
	}
	return nil
}

// Wrap informational rows so full model names, endpoints, and errors remain accessible.
func (m *model) headerItems() []headerItem {
	var rows []headerItem
	items := m.rawHeaderItems()
	labelWidth := 0
	for _, item := range items {
		if label, _, ok := strings.Cut(item.text, ": "); ok && item.command == "" {
			labelWidth = max(labelWidth, lipgloss.Width(label+":"))
		}
	}
	for _, item := range items {
		if label, value, ok := strings.Cut(item.text, ": "); ok && item.command == "" {
			item.text = cellLine(label+":", labelWidth) + "  " + value
		}
		if item.command != "" {
			rows = append(rows, item)
			continue
		}
		for _, text := range strings.Split(ansi.Wrap(item.text, max(min(58, m.termW)-4, 1), ""), "\n") {
			rows = append(rows, headerItem{text: text})
		}
	}
	return rows
}

func (m *model) closeHeader() { m.header.focused = false; m.header.menu = "" }
func (m *model) headerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	m.normalizeHeader()
	switch msg.String() {
	case "ctrl+c", "ctrl+q", "f10":
		m.quit = true
		m.cancel()
		return m, tea.Quit
	case "esc":
		if m.header.menu != "" {
			m.header.menu = ""
		} else {
			m.closeHeader()
		}
		return m, nil
	case "f9":
		m.closeHeader()
		return m, nil
	case "left", "right":
		delta := 1
		if msg.String() == "left" {
			delta = -1
		}
		badges := m.headerBadges()
		m.header.index = (m.header.index + delta + len(badges)) % len(badges)
		if m.header.menu != "" {
			m.header.menu = badges[m.header.index].id
			m.header.row = 0
		}
	case "up", "down":
		delta := 1
		if msg.String() == "up" {
			delta = -1
		}
		m.header.row = min(max(m.header.row+delta, 0), max(len(m.headerItems())-1, 0))
	case "enter":
		if m.header.menu == "" {
			m.header.menu = m.headerBadges()[min(m.header.index, len(m.headerBadges())-1)].id
			m.header.row = 0
			return m, nil
		}
		items := m.headerItems()
		if m.header.row >= len(items) {
			return m, nil
		}
		item := items[m.header.row]
		if item.enabled && item.command != "" {
			m.closeHeader()
			if item.command == "stop" {
				m.interrupted = true
				m.cancel()
				m.state = stStopping
				m.fitBottom()
				return m, nil
			}
			return m.command(item.command)
		}
	}
	return m, nil
}

func (m *model) headerGeometry() (x, width, capacity, offset int) {
	width = min(58, m.termW)
	x = 0
	for _, b := range m.headerBadges() {
		if b.id == m.header.menu {
			x = b.start
			break
		}
	}
	x = min(x, max(m.termW-width, 0))
	capacity = max(min(len(m.headerItems()), m.termH-m.footerRows()-5), 1)
	row := min(max(m.header.row, 0), max(len(m.headerItems())-1, 0))
	offset = max(row-capacity+1, 0)
	return
}
func (m *model) overlayHeader(background string) string {
	x, width, capacity, offset := m.headerGeometry()
	if width < 4 || m.termH < 6 {
		return background
	}
	normal, border, selected := popupStyles()
	rows := []string{border.Render("┌" + cellLine(" "+strings.ToUpper(m.header.menu)+" ", width-2) + "┐")}
	items := m.headerItems()
	for i := offset; i < min(offset+capacity, len(items)); i++ {
		style := normal
		prefix := "  "
		if items[i].command != "" {
			prefix = "› "
			if !items[i].enabled {
				style = border
			}
		}
		if i == m.header.row {
			style = selected
		}
		rows = append(rows, border.Render("│")+style.Render(cellLine(prefix+items[i].text, width-2))+border.Render("│"))
	}
	rows = append(rows, border.Render("└"+cellLine(" ↑↓ browse  Enter choose  Esc back", width-2)+"┘"))
	lines := strings.Split(background, "\n")
	for i, row := range rows {
		y := 2 + i
		if y >= len(lines) {
			break
		}
		lines[y] = ansi.Cut(lines[y], 0, x) + row + ansi.Cut(lines[y], x+width, m.termW)
	}
	return strings.Join(lines, "\n")
}
func (m *model) headerMouse(msg tea.MouseMsg) (bool, tea.Cmd) {
	if m.dialog != nil || m.sessions != nil || m.state == stAsk || m.state == stPermission {
		return false, nil
	}
	mouse := msg.Mouse()
	if m.actionBarVisible() && mouse.Y >= m.termH-m.actionBarRows() {
		return false, nil
	}
	if mouse.Button == tea.MouseLeft && isMouseClick(msg) && mouse.Y == 1 {
		for i, b := range m.headerBadges() {
			if mouse.X >= b.start && mouse.X < b.end {
				m.header.focused = true
				m.header.index = i
				m.header.menu = b.id
				m.header.row = 0
				return true, nil
			}
		}
	}
	if m.header.menu != "" {
		x, w, capacity, offset := m.headerGeometry()
		if mouse.Button == tea.MouseLeft && isMouseClick(msg) {
			if mouse.X >= x && mouse.X < x+w && mouse.Y >= 3 && mouse.Y < 3+capacity {
				m.header.row = offset + mouse.Y - 3
				_, cmd := m.headerKey(tea.KeyPressMsg{Code: tea.KeyEnter})
				return true, cmd
			} else {
				m.closeHeader()
			}
		}
		if mouse.Button == tea.MouseWheelUp {
			m.header.row = max(m.header.row-1, 0)
		}
		if mouse.Button == tea.MouseWheelDown {
			m.header.row = min(m.header.row+1, len(m.headerItems())-1)
		}
		return true, nil
	}
	if m.header.focused && mouse.Button == tea.MouseLeft && isMouseClick(msg) {
		m.closeHeader()
		return true, nil
	}
	return false, nil
}

func (m *model) normalizeHeader() {
	m.header.index = min(max(m.header.index, 0), len(m.headerBadges())-1)
	m.header.row = min(max(m.header.row, 0), max(len(m.headerItems())-1, 0))
}
