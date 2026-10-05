# TARS

A coding agent for small local models, in Go. It runs against koboldcpp,
llama.cpp, LM Studio, or any OpenAI-compatible API, and measures its own
work instead of trusting the model's report of it: every subtask is
verified by running a command or reading a file, repeats are refused as
errors rather than nudged, and step budgets bound every run.

Dependencies are the standard library plus `golang.org/x/sys` (Windows job
objects have no standard-library equivalent) and, for `-tui` only, the
Charm terminal stack (Bubble Tea, Lipgloss, Bubbles — pure Go, no cgo).
The agent loop, tools, and backends stay dependency-free. See `go.mod`.

## Requirements

- Go 1.25+ (see `go.mod`; CI builds on stable)
- A model backend with an OpenAI-compatible `/v1/chat/completions`
  endpoint. Local default is `http://localhost:5001`.

## Usage

```bash
go run ./cmd/agent "add a Version constant to config.go"
```

Direct mode handles single-file and question-shaped tasks. Bigger work gets
`-mission`: an upfront plan you approve, then one fresh-context worker per
subtask, each verified mechanically. Mission auto-enables when the task
names two or more deliverable files. `-plan` proposes without touching
anything; `-mode json` emits machine-readable step events; `-tui` renders
the same run fullscreen with live transcript and inline gate prompts.

All flags are in [docs/cli.md](docs/cli.md). Remote-provider keys resolve
from `-api-key`, `-api-key-env`, or environment (`TARS_API_KEY`,
`OPENAI_API_KEY`, `OPENROUTER_API_KEY`, `DEEPSEEK_API_KEY`, `GROQ_API_KEY`,
`TOGETHER_API_KEY`, `ANTHROPIC_API_KEY`).

State lands in `.tars/tasks/<id>/` after every step; `-resume` picks up
an interrupted run there.

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
- `internal/permission`, `internal/session`, `internal/snapshot`,
  `internal/events` — policy, session log, git snapshots, JSONL output

How it fits together is in [ARCHITECTURE.md](ARCHITECTURE.md). House rules
are in [docs/conventions.md](docs/conventions.md), enforced by tests.
The trust boundary is in [SECURITY.md](SECURITY.md): file tools are
guardrails against mistyped paths, not a sandbox — use a container or a VM
if process isolation matters.

## License

MIT. See [LICENSE](LICENSE).
