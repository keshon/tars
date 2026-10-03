package events

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
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
	long := strings.Repeat("x", 5000)
	got := Message(long)
	if n := len([]rune(got)); n > messageMaxRunes+1 {
		t.Fatalf("not truncated: %d runes", n)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatal("cut needs its marker")
	}
	if Message("short") != "short" {
		t.Fatal("short text altered")
	}
}

func TestMessage_RuneSafe(t *testing.T) {
	// A byte cut would split the em-dash (3 bytes in UTF-8) mid-rune.
	long := strings.Repeat("—", 5000)
	got := Message(long)
	body := strings.TrimSuffix(got, "…")
	if !utf8.ValidString(body) {
		t.Fatal("cut split a multi-byte rune")
	}
	if len([]rune(body)) != messageMaxRunes {
		t.Fatalf("cut %d runes, want %d", len([]rune(body)), messageMaxRunes)
	}
}
