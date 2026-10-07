# TARS

A coding agent for small local models, in Go. It runs against koboldcpp,
llama.cpp, LM Studio, or any OpenAI-compatible API, and measures its own
work through observed file changes and optional command checks. Mission
subtasks have explicit checks; direct runs use `-verify-cmd` for mechanical
verification. Repeat guards and step budgets bound every run.

The runtime is pure Go, without cgo. The terminal UI uses Bubble Tea v2,
Bubbles v2, Lipgloss v2, and the Charm input/ANSI helpers; Windows process
cleanup uses `golang.org/x/sys`. The agent loop and model transports do not
depend on the terminal stack. Exact versions live in [go.mod](go.mod).

## Requirements

- Go 1.26.6+ (see `go.mod`; CI uses this minimum)
- A model backend with an OpenAI-compatible `/v1/chat/completions`
  endpoint. Local default is `http://localhost:5001`.

## Usage

```bash
go run ./cmd/agent "add a Version constant to config.go"
```

Direct mode handles single-file and question-shaped tasks. Bigger work gets
`-mission`: an upfront plan you approve, then one fresh-context worker per
subtask, each verified mechanically. Mission mode is explicit; multi-file
tasks receive a recommendation. `-plan` proposes without touching
anything; `-mode json` emits machine-readable step events; `-tui` renders
the same run fullscreen with live transcript and inline gate prompts.

## Terminal UI

```bash
go run ./cmd/agent -tui
```

Start without a task to open an empty chat. Enter sends a message;
Shift+Enter adds a line, with Ctrl+O as a fallback. Type `/` for command
suggestions or `@` for workspace files. Enter runs the highlighted command
or inserts a file path; Tab only inserts. Text references include bounded
file snapshots; images require a vision-capable backend.

F1 opens categorized help. F2 opens centered session search by title and saved conversation text; wide
terminals also show a compact Sessions pane. The function-key footer exposes
Help, Search, Details, New, Mode, Sidebar, Latest, and Quit. During a run,
you can queue one follow-up, edit it, or stop the run without losing the chat.

On Windows, keep KoboldCPP in its own terminal:

1. Edit the executable/model paths in [run-kobold.cmd](run-kobold.cmd) if needed.
2. Run `run-kobold.cmd` and wait for the model to finish loading.
3. In another terminal, run `run-tui.cmd`. It checks the server and rebuilds
   `agent.exe` before opening the UI.

The supplied launcher uses Qwen3.5-9B-Q4_K_M, a 16,384-token server context,
and a 4,096-token response budget. These are launcher settings; the CLI's
response default is 8,192. Server output stays in the server terminal.
See [the terminal guide](docs/tui.md) for keys, commands, file references,
permissions, sessions, and terminal troubleshooting.

## Backend and saved runs

All flags and backend examples are in [docs/cli.md](docs/cli.md).
The [documentation index](docs/README.md) links the current guides and archived audits.
The stdio integration protocol is in [docs/rpc.md](docs/rpc.md). Remote-provider keys resolve
from `-api-key`, `-api-key-env`, or environment (`TARS_API_KEY`,
`OPENAI_API_KEY`, `OPENROUTER_API_KEY`, `DEEPSEEK_API_KEY`, `GROQ_API_KEY`,
`TOGETHER_API_KEY`, `ANTHROPIC_API_KEY`).

State lands in `<workspace>/.tars/tasks/<id>/` after every step; `-resume`
accepts its directory or state file. Ancestor and workspace `AGENTS.md`
instructions are loaded in order and refreshed on resume.

Before a Git workspace run, TARS checkpoints tracked and nonignored files
and the Git index. `-revert-preview` lists changes; `-revert` restores the
checkpoint, including pre-existing dirty and staged work. New nonignored
files are removed. Ignored files and `.tars` state are excluded; restore
refuses a changed HEAD. Use `-revert-from PATH` to select an older archive.
A workspace without a usable checkpoint receives a warning.

## Evals

`eval/probes/` holds scenarios with frozen pass criteria, scored three
ways: workspace contents, tool usage, reported answer. Each runs in a
throwaway copy of its seed workspace. See [eval/README.md](eval/README.md).

```bash
go run ./cmd/eval -dry                      # validate probes, no model calls
go run ./cmd/eval -runs 3                   # run everything
go run ./cmd/eval -only 0 -require-model qwen
```

## Layout

- `cmd/agent`, `cmd/eval` — CLI, probe runner
- `internal/agent`, `internal/mission`, `internal/roles` — the loop, the
  plan-execute-verify pipeline, the agent kinds
- `internal/llm`, `internal/tools`, `internal/prompts` — backends, tools, prompts
- `internal/api`, `internal/tui` — shared session events/gates and terminal UI
- `internal/audit`, `internal/checks` — approval records and deterministic findings
- `internal/permission`, `internal/session`, `internal/snapshot`,
  `internal/events` — policy, session log, workspace checkpoints, JSONL output

How it fits together is in [ARCHITECTURE.md](ARCHITECTURE.md). House rules
are in [docs/conventions.md](docs/conventions.md), enforced by tests.
The trust boundary is in [SECURITY.md](SECURITY.md): file tools are
guardrails against mistyped paths, not a sandbox — use a container or a VM
if process isolation matters.

## License

MIT. See [LICENSE](LICENSE).
