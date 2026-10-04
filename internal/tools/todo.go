package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/keshon/tars/internal/agent"
)

// Todo is opencode's todowrite: a local checklist the model maintains to
// keep a multi-step task straight. No permission gate and no workspace
// effect — it exists so a weak model can see its own plan instead of
// re-deriving it each step. State is in memory per process.
type Todo struct {
	mu    sync.Mutex
	items []todoItem
}

type todoItem struct {
	ID    int    `json:"id"`
	State string `json:"state"` // pending | in_progress | done
	Text  string `json:"text"`
}

// NewTodo returns an empty list.
func NewTodo() *Todo { return &Todo{} }

func (t *Todo) Name() string { return "todo" }

// Mode is Concurrent: the checklist is process-local state behind a mutex
// and touches no file, so it never needs to run alone.
func (*Todo) Mode() agent.ToolMode { return agent.Concurrent }

func (t *Todo) Description() string {
	return "Record or update a short checklist for the current task. Send the full list every " +
		"time: each item needs an id, a state (pending | in_progress | done) and text. Use it to " +
		"keep track of remaining work on a multi-step task; it changes nothing on disk."
}

func (t *Todo) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"items": {
				"type": "array",
				"description": "the complete checklist after this update",
				"items": {
					"type": "object",
					"properties": {
						"id": {"type": "integer"},
						"state": {"type": "string", "enum": ["pending", "in_progress", "done"]},
						"text": {"type": "string"}
					},
					"required": ["id", "state", "text"]
				}
			}
		},
		"required": ["items"]
	}`)
}

func (t *Todo) Run(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Items []struct {
			ID    int    `json:"id"`
			State string `json:"state"`
			Text  string `json:"text"`
		} `json:"items"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if err := rejectUnknownFields(args, "items"); err != nil {
		return "", err
	}
	if len(in.Items) > 50 {
		return "", fmt.Errorf("todo: at most 50 items, got %d", len(in.Items))
	}
	seen := map[int]bool{}
	next := make([]todoItem, 0, len(in.Items))
	for _, it := range in.Items {
		switch it.State {
		case "pending", "in_progress", "done":
		default:
			return "", fmt.Errorf("todo: item %d has state %q (want pending, in_progress or done)", it.ID, it.State)
		}
		if seen[it.ID] {
			return "", fmt.Errorf("todo: duplicate id %d", it.ID)
		}
		seen[it.ID] = true
		if strings.TrimSpace(it.Text) == "" {
			return "", fmt.Errorf("todo: item %d has empty text", it.ID)
		}
		next = append(next, todoItem{ID: it.ID, State: it.State, Text: it.Text})
	}
	t.mu.Lock()
	t.items = next
	snapshot := append([]todoItem(nil), t.items...)
	t.mu.Unlock()
	return renderTodo(snapshot), nil
}

// Items returns the current checklist.
func (t *Todo) Items() []todoItem {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]todoItem(nil), t.items...)
}

// Progress reports checklist state for the loop's finish gate: how many
// items are done, and the texts still open. Basic types only — the agent
// package must not name tool internals (tools imports agent, never the
// reverse), so the registry reaches this through a structural interface.
func (t *Todo) Progress() (done int, open []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, it := range t.items {
		if it.State == "done" {
			done++
		} else {
			open = append(open, it.Text)
		}
	}
	return done, open
}

func renderTodo(items []todoItem) string {
	if len(items) == 0 {
		return "todo: cleared"
	}
	var b strings.Builder
	b.WriteString("todo:\n")
	done := 0
	for _, it := range items {
		mark := " "
		switch it.State {
		case "done":
			mark = "x"
			done++
		case "in_progress":
			mark = ">"
		}
		fmt.Fprintf(&b, "  [%s] %d. %s\n", mark, it.ID, it.Text)
	}
	fmt.Fprintf(&b, "  (%d/%d done)\n", done, len(items))
	return b.String()
}
