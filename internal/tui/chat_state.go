package tui

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Drafts are local UI state, separate from submitted model history.
type chatDraft struct {
	text, queued, recall string
	history              []string
	index                int
}

func (m *model) saveDraft() {
	if m.drafts == nil {
		m.drafts = make(map[string]chatDraft)
	}
	m.drafts[m.stateFile] = chatDraft{m.input.Value(), m.queued, m.draft, append([]string(nil), m.hist...), m.histIdx}
}

func (m *model) restoreDraft() {
	d := m.drafts[m.stateFile]
	m.input.SetValue(d.text)
	m.queued, m.draft, m.histIdx = d.queued, d.recall, d.index
	m.hist = append([]string(nil), d.history...)
	m.fitInput()
	m.fitBottom()
}

func (m *model) resetRunFacts() {
	m.retryRun, m.runErr = nil, nil
	m.answer, m.live, m.gateDraft = "", "", ""
	m.steps, m.tokens, m.toolsUsed, m.livePainted = 0, 0, 0, 0
	m.tokensEst, m.liveCut, m.interrupted = false, false, false
	m.filesTouched = nil
	m.elapsed = 0
	m.firstToken = time.Time{}
}

func readChatMode(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "mode"))
	if err != nil {
		return "Unknown mode"
	}
	switch strings.TrimSpace(string(b)) {
	case "plan":
		return "Plan"
	case "act":
		return "Act"
	default:
		return "Unknown mode"
	}
}

func (m *model) modeName() string {
	if m.mission {
		return "Mission"
	}
	if m.plan {
		return "Plan"
	}
	return "Act"
}

func (m *model) saveMode() error {
	if m.stateFile == "" || m.mission {
		return nil
	}
	return writeChatMode(m.stateFile, m.plan)
}

func writeChatMode(stateFile string, plan bool) error {
	if err := os.MkdirAll(filepath.Dir(stateFile), 0755); err != nil {
		return err
	}
	mode := "act\n"
	if plan {
		mode = "plan\n"
	}
	return os.WriteFile(filepath.Join(filepath.Dir(stateFile), "mode"), []byte(mode), 0644)
}

func (m *model) actionHint() string {
	if m.navFocused {
		return "↑↓ choose  Enter open  Ctrl+R rename  Ctrl+D delete  Tab input"
	}
	switch m.state {
	case stPermission:
		return "PgUp/PgDn preview  Ctrl+Q quit"
	case stAsk:
		return "Enter answer  Shift+Enter newline  Esc stop"
	case stRunning:
		return "Enter queue  Shift+Enter newline  Esc stop"
	case stStopping:
		return "Stopping…  draft is preserved  Ctrl+Q quit"
	default:
		hint := "Enter send  Shift+Enter newline"
		if m.sidebarVisible() {
			hint += "  Tab switch pane"
		}
		return hint
	}
}
