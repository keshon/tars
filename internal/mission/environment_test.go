package mission

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"github.com/keshon/tars/internal/permission"
	"github.com/keshon/tars/internal/roles"
	"github.com/keshon/tars/internal/workspace"
)

func TestCheckEnvironmentScrubsKeys(t *testing.T) {
	t.Setenv("TARS_AUDIT_API_KEY", "audit-sentinel")
	cmd := "printf '%s' \"$TARS_AUDIT_API_KEY\""
	if runtime.GOOS == "windows" {
		cmd = "echo %TARS_AUDIT_API_KEY%"
	}
	out, err := RunShellCommand(context.Background(), cmd, t.TempDir())
	if err != nil || strings.Contains(out, "audit-sentinel") {
		t.Fatalf("repro failed: %v %q", err, out)
	}

}

func TestRunnerPreservesSharedEnvironment(t *testing.T) {
	e := roles.Env{BackendKind: "openai", MaxTokens: 1234, ContextLimit: 65536, Stream: true, Policy: permission.Policy{Rules: []permission.Rule{{Tool: "run_shell", Pattern: "*", Effect: permission.Deny}}}, GateContext: func(ctx context.Context, _ string, _ string, _ json.RawMessage) (permission.Effect, error) {
		return permission.Deny, ctx.Err()
	}}
	r := Runner{Env: e}
	got := r.env()
	if got.MaxTokens != 1234 || got.ContextLimit != 65536 || got.BackendKind != "openai" || !got.Stream || got.GateContext == nil || got.Policy.Evaluate("run_shell", "echo hi") != permission.Deny {
		t.Fatalf("mission environment lost: %+v", got)
	}
}

func TestMissionCheckCannotOverrideDeny(t *testing.T) {
	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	called := false
	r := Runner{WS: ws, Env: roles.Env{
		Policy: permission.Policy{Rules: []permission.Rule{{Tool: "run_shell", Pattern: "*", Effect: permission.Deny}}},
		GateContext: func(context.Context, string, string, json.RawMessage) (permission.Effect, error) {
			called = true
			return permission.Allow, nil
		},
	}}
	out, ok := r.runCheck(context.Background(), Check{Type: "shell", Cmd: "echo should-not-run"})
	if ok || called || !strings.Contains(out, "blocked by policy") {
		t.Fatalf("deny bypass: %q %v gate=%v", out, ok, called)
	}
}
