// Package audit appends gate decisions as JSONL. Opt-in (off when no
// path is configured), best-effort (a logging failure must never fail
// the gate it observes), one line per decision — the fail-open audit
// half of hook hardening.
package audit

import (
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/keshon/tars/internal/permission"
)

// Record is one gate decision.
type Record struct {
	TS       string `json:"ts"`
	Front    string `json:"front"`
	Tool     string `json:"tool"`
	Resource string `json:"resource"`
	Effect   string `json:"effect"`
	Note     string `json:"note,omitempty"`
}

// Hook wraps a gate hook so every decision appends a record. An empty
// path disables logging (passthrough). The operator note rides the
// "blocked by operator: ..." denial text; any other error is recorded
// whole as the decision's reason.
func Hook(path, front string, inner func(tool, resource string, args json.RawMessage) (permission.Effect, error)) func(tool, resource string, args json.RawMessage) (permission.Effect, error) {
	if path == "" {
		return inner
	}
	return func(tool, resource string, args json.RawMessage) (permission.Effect, error) {
		eff, err := inner(tool, resource, args)
		rec := Record{
			TS:       time.Now().UTC().Format(time.RFC3339),
			Front:    front,
			Tool:     tool,
			Resource: resource,
			Effect:   eff.String(),
		}
		if err != nil {
			rec.Note = strings.TrimSpace(strings.TrimPrefix(err.Error(), "blocked by operator:"))
			if rec.Note == "" && !strings.Contains(err.Error(), "blocked by operator") {
				rec.Note = err.Error()
			}
		}
		if data, merr := json.Marshal(rec); merr == nil {
			f, oerr := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if oerr == nil {
				_, _ = f.Write(append(data, '\n'))
				_ = f.Close()
			}
		}
		return eff, err
	}
}
