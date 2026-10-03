// Package events emits one JSON object per line (JSONL) so a run can be
// consumed by tooling instead of scraped from console text. Nothing here
// is required for the agent to work: it is observability, and it is
// written to stderr-free stdout so a caller can pipe it.
package events

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// Emitter writes JSONL events to a writer. Nil-safe: a zero Emitter drops
// everything, so callers never branch on whether output is enabled.
type Emitter struct {
	mu  sync.Mutex
	w   io.Writer
	seq int
}

// New returns an Emitter writing to w.
func New(w io.Writer) *Emitter { return &Emitter{w: w} }

// Emit writes one event with an auto-incrementing seq. Errors are
// dropped: a broken pipe on stdout must not abort a run.
func (e *Emitter) Emit(kind string, fields map[string]any) {
	if e == nil || e.w == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seq++
	rec := map[string]any{"seq": e.seq, "event": kind}
	for k, v := range fields {
		rec[k] = v
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	fmt.Fprintf(e.w, "%s\n", data)
	if f, ok := e.w.(interface{ Sync() error }); ok {
		_ = f.Sync()
	}
}

// messageMaxRunes caps one event text field. It bounds runaway model
// output on the wire (JSONL log, TUI, RPC share the funnel), not routine
// prose: answers and deliberation regularly pass 400 bytes, and the TUI's
// expanded-think view is pointless if reasoning arrives pre-cut. Cuts
// keep their "…" marker per R5 (plan P10).
const messageMaxRunes = 4000

// Message renders a tool call's arguments compactly for an event.
// The cut is rune-based: a byte slice can split multi-byte UTF-8 and
// emit invalid output.
func Message(s string) string {
	if runes := []rune(s); len(runes) > messageMaxRunes {
		return string(runes[:messageMaxRunes]) + "…"
	}
	return s
}
