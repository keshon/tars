package tui

import (
	"os"
	"path/filepath"
	"sort"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/keshon/tars/internal/agent"
	"github.com/keshon/tars/internal/llm"
	"github.com/keshon/tars/internal/session"
	"github.com/keshon/tars/internal/workspace"
)

type cachedSession struct {
	entry       sessionEntry
	size        int64
	modified    time.Time
	searchable  bool
	storedTitle string
}
type sessionsLoadedMsg struct {
	revision uint64
	root     string
	entries  []sessionEntry
	cache    map[string]cachedSession
}

// Commands capture immutable inputs. Only Update accepts their results into UI state.
func (m *model) refreshNavigator() {
	m.sessionRevision++
	m.nav.loading = true
	if m.sessions != nil {
		m.sessions.loading = true
	}
	revision, root, cache, search := m.sessionRevision, m.sessionRoot(), m.sessionIndex, m.sessions != nil
	m.pendingCommands = append(m.pendingCommands, func() tea.Msg {
		entries, index := scanSessions(root, cache, search)
		return sessionsLoadedMsg{revision, root, entries, index}
	})
}
func (m *model) takeCommands() tea.Cmd {
	commands := m.pendingCommands
	m.pendingCommands = nil
	return tea.Batch(commands...)
}
func (m *model) acceptSessions(msg sessionsLoadedMsg) {
	if msg.revision != m.sessionRevision || msg.root != m.sessionRoot() {
		return
	}
	selected := ""
	if len(m.nav.entries) > 0 {
		selected = m.nav.entries[min(m.nav.cursor, len(m.nav.entries)-1)].dir
	}
	m.nav.entries, m.sessionIndex = msg.entries, msg.cache
	m.nav.loading = false
	for i, e := range m.nav.entries {
		if e.dir == selected {
			m.nav.cursor = i
			break
		}
	}
	m.nav.clamp(m.navigatorRows())
	if m.sessions != nil {
		m.sessions.replaceEntries(msg.entries)
		m.sessions.loading = false
	}
}

// Histories are decoded only when changed. Search text is prepared on demand;
// the ordinary navigator retains metadata, not a second copy of every transcript.
func scanSessions(root string, previous map[string]cachedSession, search bool) ([]sessionEntry, map[string]cachedSession) {
	dirs, err := os.ReadDir(root)
	if err != nil {
		return nil, nil
	}
	cache := make(map[string]cachedSession, len(dirs))
	var entries []sessionEntry
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		dir := filepath.Join(root, d.Name())
		title, mode := workspace.ReadSessionTitle(dir), readChatMode(dir)
		state := filepath.Join(dir, "state.json")
		fi, stateErr := os.Stat(state)
		old, ok := previous[dir]
		current := cachedSession{storedTitle: title, entry: sessionEntry{id: d.Name(), dir: dir, title: title, mode: mode, status: "Saved"}}
		if stateErr == nil {
			current.size, current.modified = fi.Size(), fi.ModTime()
			if ok && old.storedTitle == title && old.size == current.size && old.modified.Equal(current.modified) && (!search || old.searchable) {
				current = old
				current.entry.mode = mode
				if title != "" {
					current.entry.title = title
				}
			} else {
				current.entry.status = "Unreadable"
				current.entry.updated = fi.ModTime()
				if fi.Size() < 64<<20 {
					if history, err := agent.LoadState(state); err == nil {
						e := &current.entry
						e.status, e.msgs = "Saved", len(history)
						if search {
							for i, b := range renderHistory(history) {
								if b.role == roleUser || b.role == roleAnswer {
									e.search = append(e.search, sessionText{text: b.text, block: i})
								}
							}
							current.searchable = true
						}
						if _, question, paused := agent.PausedOnQuestion(history); paused {
							e.status, e.preview = "Needs input", question
						} else {
							for i := len(history) - 1; i >= 0; i-- {
								if history[i].Role == llm.RoleAssistant && history[i].Content != "" {
									e.preview = truncate(history[i].Content, 500)
									break
								}
							}
						}
						if e.title == "" {
							e.title = session.TitleFor(history)
						}
					}
				}
			}
		}
		if fi, err := os.Stat(filepath.Join(dir, "mission.json")); err == nil {
			current.entry.mode = "Mission"
			if current.entry.updated.IsZero() {
				current.entry.updated = fi.ModTime()
			}
			current.entry.preview = "Mission snapshot. Resume with agent -tui -resume " + dir
		}
		if current.entry.updated.IsZero() {
			continue
		}
		if current.entry.title == "" {
			current.entry.title = current.entry.id
		}
		cache[dir] = current
		entries = append(entries, current.entry)
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].updated.After(entries[j].updated) })
	return entries, cache
}
