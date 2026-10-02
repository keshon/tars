# TARS

A coding agent for local LLMs, in Go.

Dependencies are kept to the standard library plus `golang.org/x/sys`, which is
needed for Windows job objects — there is no standard-library way to kill a
process tree whose parent has already exited. A third-party package is
considered only if it is cgo-free, popular, and does something worth the weight.

Aimed at small local models (12–35B) served by koboldcpp, llama.cpp or LM Studio.
Correctness comes from the harness — mechanical checks, hard repeat guards, bounded
budgets — not from trusting the model's report of its own work.

## Requirements

- Go 1.22+
- A model backend: either a local server with an OpenAI-compatible
  `/v1/chat/completions` endpoint, listening on `http://localhost:5001` by
  default, or a remote OpenAI-compatible API (see Remote providers below).

## Usage

```bash
go run ./cmd/agent "add a Version constant to config.go"
```

Common flags:

| Flag | Meaning |
|---|---|
| `-workspace DIR` | Root the agent may read and write. Default `.` |
| `-mission` | Plan first, then run one fresh-context worker per subtask |
| `-yes` | Skip the mission plan approval gate |
| `-verify-cmd CMD` | Command run at the self-check point, e.g. `"go test ./..."` |
| `-resume PATH` | Continue an interrupted run from its saved state |
| `-debug` | Write raw request/response JSON to `agent-debug.log` |
| `-backend URL` | Backend base URL |
| `-backend-kind KIND` | `kobold`, `llama`, or `openai` (any hosted OpenAI-compatible API) |
| `-model NAME` | Model name; required for remote providers |
| `-api-key KEY` | Bearer token for a remote provider (prefer env vars, see below) |
| `-api-key-env NAME` | Env var holding the bearer token, e.g. `OPENROUTER_API_KEY` |
| `-context-limit N` | Context window override; remote backends default to a per-model estimate |
| `-allow RULES` | Comma-separated `tool=pattern` rules to allow, e.g. `"run_shell=go *,read_file=*.go"` |
| `-deny RULES` | Comma-separated `tool=pattern` rules to deny, e.g. `"run_shell=rm *,read_file=.env"` |
| `-pure` | Ignore project config for permissions; defaults plus `-allow`/`-deny` only |
| `-stream` | Stream response tokens live (openai/llama only; koboldcpp falls back to unary) |
| `-mcp SERVERS` | MCP servers: `"name=cmd args...;name2=cmd2"` (tools appear as `mcp__name__tool`) |
| `-fork PATH` | Branch from a prior transcript file but write to a fresh task id |
| `-revert` | Restore tracked workspace files to git HEAD and exit (untracked files kept) |

## Remote providers

Any provider speaking the OpenAI chat format works through `-backend-kind openai`.
The key is resolved as `-api-key`, then `-api-key-env`, then the first token found
under `TARS_API_KEY`, `OPENAI_API_KEY`, `OPENROUTER_API_KEY`, `DEEPSEEK_API_KEY`,
`GROQ_API_KEY` or `TOGETHER_API_KEY`.

```bash
export OPENAI_API_KEY=sk-...
go run ./cmd/agent -backend-kind openai -backend https://api.openai.com/v1 -model gpt-4o-mini "add a Version constant to config.go"

export OPENROUTER_API_KEY=sk-or-...
go run ./cmd/agent -backend-kind openai -backend https://openrouter.ai/api/v1 -model anthropic/claude-sonnet-4 "add a Version constant to config.go"
```

Remote backends report no context window, so the budget falls back to a per-model
estimate unless `-context-limit` says otherwise. Structured calls (mission plans,
verdicts) use `response_format` instead of GBNF grammar on this path; everything
else — tools, repeat guards, budgets — behaves the same.

A `run-remote.cmd` launcher (gitignored, lives only on this machine) holds the
OpenRouter key and these settings, so a run is just `run-remote.cmd "task"`.

## Modes

**Direct** — one agent, one context, tools until done. For single-file and
question-shaped tasks.

**Mission** (`-mission`) — a grammar-constrained plan you approve, then one worker
per subtask, each starting from a compiled seed rather than the previous worker's
transcript. Every subtask is verified mechanically after it runs. Auto-enabled when
a task names two or more files.

State is written to `.agent/tasks/<id>/` after every step; `-resume` picks up there.

## Evals

`eval/probes/` holds scenarios with frozen pass criteria. Each runs in a throwaway
copy of its seed workspace and is scored three ways: what the workspace contains,
which tools were used, and what the run reported.

```bash
go run ./cmd/eval -dry                      # validate probes, no model calls
go run ./cmd/eval -runs 3                   # run everything
go run ./cmd/eval -only 0 -require-model qwen
```

Results, per-run traces and the model that produced them land in
`eval/results/<timestamp>/`. `-require-model` refuses to start unless the backend
reports the weights you expect.

`eval/prompts/` holds the human-readable write-up each probe mechanizes.

## Layout

```
cmd/agent        CLI
cmd/eval         probe runner
internal/agent   the loop: tools, repeat detection, budgets, compaction
internal/mission plan, ledger, workers, checks, replan, review
internal/roles   the four kinds of agent this project builds
internal/llm     backend client, GBNF grammar
internal/tools   file, shell, search, process and delegation tools
internal/prompts every prompt, as .txt
```

## Limitations

- Tuned for weak models. Larger tasks fail as bad plans, not bad code.
- `internal/workspace` bounds the file tools, not the process. `run_shell` executes
  arbitrary commands. Use a container or a VM if that matters.
- koboldcpp performs its own tool-call decision pass, which this harness does not
  control.
- Windows-first; the shell tools pick `cmd.exe` or `sh` by OS but see less testing
  on Linux and macOS.

## License

MIT. See [LICENSE](LICENSE).
