// Package session appends an immutable JSONL event log next to state.json.
// state.json is the resume snapshot (rewritten atomically each step);
// state.jsonl retains full recovery checkpoints, with one 16 MiB previous log.
// A corrupt snapshot can be rebuilt from the log; a corrupt log line is
// skipped, never fatal.
package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// Append writes a bounded recovery log and returns persistence failures.
// Every entry is a full checkpoint,
// so rotation does not make recovery depend on a missing earlier delta.
func Append(stateFile string, seq int, history []llm.Message) error {
	if stateFile == "" {
		return nil
	}
	path := LogPath(stateFile)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	ev := Event{Seq: seq, Time: time.Now().UTC().Format(time.RFC3339), Messages: len(history), History: history}
	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if info, err := os.Stat(path); err == nil && info.Size()+int64(len(data)) > 16*1024*1024 {
		if err := os.Remove(path + ".previous"); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.Rename(path, path+".previous"); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(f, string(data))
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
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
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read session log: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("session log %s holds no valid events", path)
	}
	return last, nil
}

// TitleFor derives the default display title: the first user message,
// first line, trimmed, capped at 60 runes. Loop nudges ([harness]
// provenance) are history, not conversation, and never title.
// Shapes locally instead of sharing workspace.TitleLine:
// package-layers forbids session from importing workspace, and the
// twin is eight lines, not a seam.
func TitleFor(history []llm.Message) string {
	for _, msg := range history {
		if msg.Role != llm.RoleUser || strings.TrimSpace(msg.Content) == "" {
			continue
		}
		if strings.HasPrefix(msg.Content, "[harness] ") {
			continue
		}
		line := msg.Content
		if i := strings.IndexByte(line, '\n'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(strings.ReplaceAll(line, "\r", ""))
		if r := []rune(line); len(r) > 60 {
			line = string(r[:60])
		}
		return line
	}
	return ""
}
