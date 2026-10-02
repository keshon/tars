// Package session appends an immutable JSONL event log next to state.json.
// state.json is the resume snapshot (rewritten atomically each step);
// state.jsonl is the audit trail (one line per step, never rewritten).
// A corrupt snapshot can be rebuilt from the log; a corrupt log line is
// skipped, never fatal.
package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/keshon/tars/internal/llm"
)

// Event is one appended step.
type Event struct {
	Seq      int           `json:"seq"`
	Time     string        `json:"time"`
	Messages int           `json:"messages"`
	History  []llm.Message `json:"history"`
}

// LogPath returns the .jsonl path next to a state.json path.
func LogPath(stateFile string) string {
	ext := filepath.Ext(stateFile)
	if ext == "" {
		return stateFile + ".jsonl"
	}
	return stateFile[:len(stateFile)-len(ext)] + ".jsonl"
}

// Append writes one event for the current history. Best-effort like
// saveState: logging must never break a working run.
func Append(stateFile string, seq int, history []llm.Message) {
	if stateFile == "" {
		return
	}
	path := LogPath(stateFile)
	if dir := filepath.Dir(path); dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	ev := Event{Seq: seq, Time: time.Now().UTC().Format(time.RFC3339), Messages: len(history), History: history}
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintln(f, string(data))
}

// Load reads the last event's history from a .jsonl log, skipping corrupt
// lines. Returns an error only when no valid event exists.
func Load(path string) ([]llm.Message, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read session log: %w", err)
	}
	defer f.Close()
	var last []llm.Message
	found := false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for sc.Scan() {
		var ev Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			continue
		}
		if ev.History != nil {
			last = ev.History
			found = true
		}
	}
	if !found {
		return nil, fmt.Errorf("session log %s holds no valid events", path)
	}
	return last, nil
}
