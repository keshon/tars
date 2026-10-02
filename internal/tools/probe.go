package tools

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

// probeTimeout bounds one existence probe. Validation runs it per plan,
// so it must stay far below the worker's own check timeout.
const probeTimeout = 5 * time.Second

// ProbeResult is what `name` proved by actually running.
type ProbeResult struct {
	// Status is one of "ok", "missing", "broken" or "timeout".
	Status string
	// Hint names the fix when Status is not ok. Empty when ok.
	Hint string
}

// ProbeCommand proves a binary runs instead of trusting that a file with
// its name exists. exec.LookPath succeeding only means something is on
// PATH under that name — stale pipx/uv shims point at deleted
// interpreters and die at start with 126/127, long after LookPath said
// yes. The probe runs `name` with args (conventionally `--version`,
// which well-behaved tools answer without side effects) and reports what
// happened:
//
//   - the process started → ok, whatever it exited with. A `--version`
//     flag the tool does not know still proves the tool is there.
//   - it failed to start, or exited 126/127 → broken, with a reinstall hint.
//   - LookPath finds nothing → missing.
//   - it outlived the timeout → timeout. A check that hangs its validator
//     would hang the worker too, so this is a verdict, not an error.
func ProbeCommand(ctx context.Context, name string, args ...string) ProbeResult {
	if _, err := exec.LookPath(name); err != nil {
		return ProbeResult{
			Status: "missing",
			Hint:   fmt.Sprintf("%q is not on PATH — install it or use a command this host has", name),
		}
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	err := cmd.Run()
	if err == nil {
		return ProbeResult{Status: "ok"}
	}
	if ctx.Err() == context.DeadlineExceeded {
		return ProbeResult{
			Status: "timeout",
			Hint:   fmt.Sprintf("%q did not answer within %s — a check starting it would hang the worker", name, probeTimeout),
		}
	}
	if exit, ok := err.(*exec.ExitError); ok {
		if code := exit.ExitCode(); code == 126 || code == 127 {
			return ProbeResult{
				Status: "broken",
				Hint: fmt.Sprintf("%q exits %d: a stale shim or broken install. "+
					"Reinstall it (uv tool install --force <pkg> / pipx reinstall <pkg>) "+
					"or use a command that runs", name, code),
			}
		}
		// Started and answered with its own exit code: the binary is
		// real, even if it disliked our probe flag.
		return ProbeResult{Status: "ok"}
	}
	// Anything else means the OS never ran it: permission denied, stale
	// shebang, missing DLL, "not a valid Win32 application". LookPath
	// said yes and execution says no — that contradiction is the finding.
	return ProbeResult{
		Status: "broken",
		Hint:   fmt.Sprintf("%q cannot start (%v). Reinstall it or use a command that runs", name, err),
	}
}
