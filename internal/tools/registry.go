package tools

import (
	"github.com/keshon/tars/internal/agent"
	"github.com/keshon/tars/internal/workspace"
)

// Base returns the standard tool set shared by the main agent and
// subagents. Pass extra tools (ask_user, delegate_task, todo) for the main
// agent only. File-mutating tools run post-write deterministic checks
// (gofmt, secret scan) reporting to sink; nil sink builds unwrapped.
func Base(ws *workspace.Workspace, procs *BackgroundProcesses, report FindingSink, extra ...agent.Tool) *agent.Registry {
	scan := func(t agent.Tool) agent.Tool { return Checked(t, ws, report) }
	tools := []agent.Tool{
		ReadFile{WS: ws},
		scan(WriteFile{WS: ws}),
		scan(PatchFile{WS: ws}),
		scan(PatchLines{WS: ws}),
		ListFiles{WS: ws},
		MoveFile{WS: ws},
		RunShell{WS: ws},
		GrepFiles{WS: ws},
		StartBackground{WS: ws, Procs: procs},
		CheckBackground{Procs: procs},
		StopBackground{Procs: procs},
		CheckURL{},
		Webfetch{WS: ws},
		&Todo{},
	}
	tools = append(tools, extra...)
	return agent.NewRegistry(tools...)
}

// ReadOnly returns a tool set that can inspect the workspace and running
// processes but cannot change anything — no writes, no shell, no process
// control. For workers whose job is to look and report (codebase mapping,
// final review): removing the mutating tools entirely beats prompting a
// weak model not to use them.
func ReadOnly(ws *workspace.Workspace, procs *BackgroundProcesses) *agent.Registry {
	return agent.NewRegistry(
		ReadFile{WS: ws},
		ListFiles{WS: ws},
		GrepFiles{WS: ws},
		CheckBackground{Procs: procs},
		CheckURL{},
		Webfetch{WS: ws},
	)
}
