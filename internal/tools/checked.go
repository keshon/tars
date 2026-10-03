package tools

import (
	"context"
	"encoding/json"
	"os"

	"github.com/keshon/tars/internal/agent"
	"github.com/keshon/tars/internal/checks"
	"github.com/keshon/tars/internal/workspace"
)

// FindingSink receives deterministic post-write findings. Nil disables
// scanning. Primitive signature keeps roles/session/cli free of new
// imports: detectors live here, delivery lives with the caller.
type FindingSink func(rule, path string, line int, summary string)

// checked decorates a file-mutating tool with post-write deterministic
// checks (gofmt, secret scan). Report-only: findings ride the sink,
// the tool result is untouched, and every failure mode (tool error,
// unparseable args, missing path, unreadable file) returns the inner
// result unchanged — fail open, never block the run on the check.
type checked struct {
	inner  agent.Tool
	ws     *workspace.Workspace
	report FindingSink
}

// Checked wraps inner with post-write scans, or returns it unchanged
// when there is nowhere to report to (nil sink) or nothing to resolve
// against (nil workspace). Zero behavior change unless configured.
func Checked(inner agent.Tool, ws *workspace.Workspace, report FindingSink) agent.Tool {
	if report == nil || ws == nil {
		return inner
	}
	return &checked{inner: inner, ws: ws, report: report}
}

func (c *checked) Name() string        { return c.inner.Name() }
func (c *checked) Description() string { return c.inner.Description() }
func (c *checked) Schema() json.RawMessage {
	return c.inner.Schema()
}
func (c *checked) Mode() agent.ToolMode { return c.inner.Mode() }

// Idempotent forwards only when the inner tool declares it: the wrapper
// must never upgrade a mutating tool to idempotent by accident.
func (c *checked) Idempotent() bool {
	if it, ok := c.inner.(interface{ Idempotent() bool }); ok {
		return it.Idempotent()
	}
	return false
}

func (c *checked) Run(ctx context.Context, args json.RawMessage) (string, error) {
	out, err := c.inner.Run(ctx, args)
	if err != nil {
		return out, err
	}
	var in struct {
		Path string `json:"path"`
	}
	if jerr := json.Unmarshal(args, &in); jerr != nil || in.Path == "" {
		return out, err
	}
	full, rerr := c.ws.Resolve(in.Path)
	if rerr != nil {
		return out, err
	}
	data, rerr := os.ReadFile(full)
	if rerr != nil {
		return out, err
	}
	// Shell redirections, MCP writes, and background jobs bypass this:
	// only tool-written content addressed by "path" is scanned. The
	// session-end sweep covers side-effect files by path instead.
	for _, f := range checks.ScanFile(in.Path, data) {
		c.report(f.Rule, f.Path, f.Line, f.Summary)
	}
	return out, err
}
