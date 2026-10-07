package tui

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	"github.com/keshon/tars/internal/llm"
)

type usageSnapshot struct {
	Tokens    int       `json:"tokens"`
	Estimated bool      `json:"estimated"`
	At        time.Time `json:"at"`
	Model     string    `json:"model"`
}

func safeEndpoint(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return ""
	}
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	return u.String()
}

func (m *model) clearContext() {
	m.tokens, m.tokensEst, m.contextSource = 0, false, ""
	m.usageAt, m.lastUsage, m.usageThisRun = time.Time{}, usageSnapshot{}, false
}

func (m *model) restoreContext(history []llm.Message) {
	m.clearContext()
	chars := 0
	for _, msg := range history {
		// Reasoning is stored for display but is not replayed to the backend.
		chars += utf8.RuneCountInString(msg.Content) + 16
		for _, call := range msg.ToolCalls {
			chars += utf8.RuneCountInString(call.Name) + utf8.RuneCount(call.Arguments)
		}
	}
	if chars > 0 {
		m.tokens, m.tokensEst = (chars+3)/4, true
		m.contextSource = "Saved history estimate"
	}
	if m.stateFile != "" {
		data, err := os.ReadFile(filepath.Join(filepath.Dir(m.stateFile), "usage.json"))
		var saved usageSnapshot
		if err == nil && json.Unmarshal(data, &saved) == nil && saved.Tokens > 0 && saved.Model == m.modelName {
			m.lastUsage = saved
		}
	}
}

func (m *model) persistUsage() {
	if !m.usageThisRun || m.stateFile == "" {
		return
	}
	data, err := json.Marshal(m.lastUsage)
	if err == nil {
		_ = os.WriteFile(filepath.Join(filepath.Dir(m.stateFile), "usage.json"), data, 0644)
	}
}
