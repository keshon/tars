package events

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestEmit_JSONLines(t *testing.T) {
	var buf bytes.Buffer
	e := New(&buf)
	e.Emit("step", map[string]any{"step": 1, "text": "hi"})
	e.Emit("step", map[string]any{"step": 2})
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines=%d", len(lines))
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("not json: %v", err)
	}
	if rec["event"] != "step" || rec["seq"] != float64(1) {
		t.Fatalf("rec=%v", rec)
	}
	if err := json.Unmarshal([]byte(lines[1]), &rec); err != nil {
		t.Fatalf("not json: %v", err)
	}
	if rec["seq"] != float64(2) {
		t.Fatalf("seq not incremented: %v", rec)
	}
}

func TestNilEmitter_Drops(t *testing.T) {
	var e *Emitter
	e.Emit("x", nil) // must not panic
}

func TestMessage_Truncates(t *testing.T) {
	long := strings.Repeat("x", 500)
	got := Message(long)
	if len([]rune(got)) > 401 {
		t.Fatalf("not truncated: %d", len([]rune(got)))
	}
	if Message("short") != "short" {
		t.Fatal("short text altered")
	}
}
