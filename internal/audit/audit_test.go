package audit

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keshon/tars/internal/permission"
)

func TestHook_DisabledIsPassthrough(t *testing.T) {
	calls := 0
	inner := func(tool, resource string, args json.RawMessage) (permission.Effect, error) {
		calls++
		return permission.Allow, nil
	}
	hooked := Hook("", "cli", inner)
	if eff, err := hooked("read_file", "a", json.RawMessage(`{}`)); eff != permission.Allow || err != nil {
		t.Fatalf("passthrough = %v, %v", eff, err)
	}
	if calls != 1 {
		t.Fatalf("inner called %d times", calls)
	}
}

func TestHook_RecordsDecision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	inner := func(tool, resource string, args json.RawMessage) (permission.Effect, error) {
		return permission.Deny, errors.New("blocked by operator: use cat instead")
	}
	hooked := Hook(path, "tui", inner)
	if _, err := hooked("run_shell", "rm x", json.RawMessage(`{}`)); err == nil {
		t.Fatal("hook must return the inner result")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rec Record
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &rec); err != nil {
		t.Fatalf("not json: %v", err)
	}
	if rec.Front != "tui" || rec.Tool != "run_shell" || rec.Effect != "deny" || rec.Note != "use cat instead" {
		t.Fatalf("record = %+v", rec)
	}
}

func TestHook_LogFailureNeverBreaksGate(t *testing.T) {
	inner := func(tool, resource string, args json.RawMessage) (permission.Effect, error) {
		return permission.Allow, nil
	}
	hooked := Hook(filepath.Join(t.TempDir(), "no-such-dir", "a.jsonl"), "cli", inner)
	if eff, err := hooked("x", "y", json.RawMessage(`{}`)); eff != permission.Allow || err != nil {
		t.Fatalf("logging failure broke the gate: %v, %v", eff, err)
	}
}
