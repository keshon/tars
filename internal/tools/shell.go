package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/keshon/tars/internal/agent"
	"github.com/keshon/tars/internal/workspace"
)

type RunShell struct {
	WS      *workspace.Workspace
	Timeout time.Duration // defaults to 30s if zero
}

func (RunShell) Name() string { return "run_shell" }

// Mode is Exclusive: an arbitrary shell command could do anything
// (mutate files, hold a lock, depend on ordering) — there's no way to
// tell from the string alone, so the safe default is to never run it
// alongside other tool calls in the same step.
func (RunShell) Mode() agent.ToolMode { return agent.Exclusive }
func (RunShell) Description() string {
	if runtime.GOOS == "windows" {
		return "Run a command inside the workspace via cmd.exe (use Windows commands: " +
			"ren, copy, move, del, git, go, npm, etc). For listing directory contents use " +
			"list_files instead of dir. Return its combined output."
	}
	return "Run a command inside the workspace via sh (use POSIX commands: " +
		"mv, cp, rm, git, go, npm, etc). For listing directory contents use " +
		"list_files instead of ls. For searching file text use grep_files " +
		"instead of grep. Return combined stdout+stderr."
}
func (RunShell) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {"command": {"type": "string"}},
		"required": ["command"]
	}`)
}

func (t RunShell) Run(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}

	timeout := t.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	full, err := RunCommand(ctx, in.Command, t.WS.Root(), timeout)
	result := capShellOutput(full)

	if err != nil {
		return result, fmt.Errorf("command failed: %w", err)
	}
	return result, nil
}

// shellMaxBytes caps one run_shell result, matching the read_file cap for
// the same reason: no single tool result may consume most of a small
// context window.
//
// This was the last uncapped way into the context, and capping the others
// is what found it. read_file truncates and grep_files caps its matches,
// so a model denied a large read reaches for the shell instead: live, a
// probe that reads a 205KB fixture ran "type big.txt", put the whole file
// in the prompt, and drove a 3.4k prompt to 57k in one step. The read cap
// had been tightened an hour earlier and was simply not in the path.
//
// Middle-truncated rather than head-truncated: a command's first lines
// carry what it is doing and its last lines carry how it failed, and the
// failure is usually the point.
const shellMaxBytes = 48 * 1024

func capShellOutput(s string) string {
	if len(s) <= shellMaxBytes {
		return s
	}
	return agent.TruncateMiddle(s, shellMaxBytes) +
		fmt.Sprintf("\n\n(output truncated: %d bytes total. Re-run narrowed — a pipe through "+
			"findstr/grep, a smaller path, or head/tail — rather than asking for all of it again.)",
			len(s))
}

// scrubEnv removes bearer tokens from the child environment so a model
// that prints env or exfils it via curl gets nothing useful.
func scrubEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k := kv
		if i := strings.Index(kv, "="); i >= 0 {
			k = kv[:i]
		}
		upper := strings.ToUpper(k)
		if strings.Contains(upper, "API_KEY") || strings.Contains(upper, "APIKEY") ||
			strings.Contains(upper, "AUTH_TOKEN") || strings.Contains(upper, "SECRET") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// shellCommand picks a shell that actually understands the commands a
// model writes for this host OS. On Windows that's cmd.exe — ren, dir,
// copy and friends are cmd.exe builtins with no standalone executable, so
// running them through a POSIX sh (even one present via Git Bash) just
// fails with "command not found".
func shellCommand(ctx context.Context, command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "cmd", "/C", command)
	}
	return exec.CommandContext(ctx, "sh", "-c", command)
}
