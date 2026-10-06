package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/keshon/tars/internal/agent"
	"github.com/keshon/tars/internal/workspace"
)

// StartBackground launches a long-running command (dev server, watcher)
// without blocking on it. This exists specifically because run_shell
// waits for the command to exit — correct for "go build", wrong for
// "npm run dev", which never exits by design and gets killed the moment
// a blocking call's timeout fires.
type StartBackground struct {
	WS *workspace.Workspace
	// Procs must be the same *BackgroundProcesses pointer shared with
	// CheckBackground and StopBackground.
	Procs *BackgroundProcesses
	// SettleTime is how long to wait before returning, so a command that
	// fails immediately (port already in use, command not found) shows
	// that in the result instead of falsely reporting "running". Zero
	// defaults to 1.5s.
	SettleTime time.Duration
}

func (StartBackground) Name() string { return "start_background" }

// Starting an arbitrary command is a mutation barrier.
func (StartBackground) Mode() agent.ToolMode { return agent.Exclusive }
func (StartBackground) Description() string {
	return "Start a long-running command (dev server, watcher, anything that doesn't exit on " +
		"its own) without blocking. Returns an id to check on it with check_background or stop " +
		"it with stop_background. Use this instead of run_shell for anything like 'npm run dev' " +
		"— run_shell will time out and kill it before you can test anything."
}
func (StartBackground) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {"command": {"type": "string"}},
		"required": ["command"]
	}`)
}

func (t StartBackground) Run(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}

	// Deliberately context.Background(), not the tool call's ctx — this
	// process must outlive the call that started it.
	cmd := shellCommand(context.Background(), in.Command)
	cmd.Dir = t.WS.Root()
	cmd.Env = scrubEnv(os.Environ())
	id, _ := t.Procs.start(cmd)

	settle := t.SettleTime
	if settle == 0 {
		settle = 1500 * time.Millisecond
	}
	timer := time.NewTimer(settle)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		_ = t.Procs.stop(id)
		return "", ctx.Err()
	}

	output, exited, exitErr, _ := t.Procs.status(id)
	status := "running in background"
	if exited {
		status = fmt.Sprintf("already exited: %v", exitErr)
	}
	return fmt.Sprintf("id: %s\nstatus: %s\noutput so far:\n%s", id, status, output), nil
}

type CheckBackground struct{ Procs *BackgroundProcesses }

func (CheckBackground) Name() string         { return "check_background" }
func (CheckBackground) Mode() agent.ToolMode { return agent.Concurrent }
func (CheckBackground) Description() string {
	return "Check the current output and status (running/exited) of a process started with " +
		"start_background."
}
func (CheckBackground) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {"id": {"type": "string"}},
		"required": ["id"]
	}`)
}

func (t CheckBackground) Run(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	output, exited, exitErr, err := t.Procs.status(in.ID)
	if err != nil {
		return "", err
	}
	status := "still running"
	if exited {
		status = "exited cleanly"
		if exitErr != nil {
			status = fmt.Sprintf("exited with error: %v", exitErr)
		}
	}
	return fmt.Sprintf("status: %s\noutput:\n%s", status, output), nil
}

type StopBackground struct{ Procs *BackgroundProcesses }

func (StopBackground) Name() string         { return "stop_background" }
func (StopBackground) Mode() agent.ToolMode { return agent.Concurrent }
func (StopBackground) Description() string  { return "Stop a process started with start_background." }
func (StopBackground) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {"id": {"type": "string"}},
		"required": ["id"]
	}`)
}

func (t StopBackground) Run(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if err := t.Procs.stop(in.ID); err != nil {
		return "", err
	}
	return "stopped", nil
}
