# CLI reference

Every flag of `cmd/agent`, grouped by purpose. A conventions test
(`internal/conventions`, tag `cli-flags`) fails if a flag exists in code
but is missing here, so this page cannot drift behind the binary.

## Backend

| Flag | Default | Meaning |
|---|---|---|
| `-backend URL` | `http://localhost:5001` | Backend base URL: bare origin for a local server, API root for a hosted OpenAI-compatible provider |
| `-backend-kind KIND` | `kobold` | `kobold`, `llama`, or `openai`. A mismatch is caught at startup: the dialects disagree about grammar and sampler fields, so a wrong value measures nothing |
| `-model NAME` | `local` | Model name. Ignored by most local servers (they serve whatever weights they started with); required for remote providers |
| `-context-limit N` | `0` | Context window override. Remote backends expose no probe endpoint and fall back to a per-model estimate unless set |
| `-grammar` | `true` | Apply the backend's own content grammar (blocks a model writing its native tool-call tags as plain text on koboldcpp) |
| `-max-tokens N` | `8192` | Generation budget per response. Too low truncates large outputs mid-JSON, which looks like a model failure |
| `-max-steps N` | `0` (=25) | Step budget per run. Applies to direct runs; mission workers and subagents keep fixed budgets |
| `-reasoning-budget N` | `0` (=6000) | Characters of `<think>` deliberation allowed per response before a wrap-up round demands commitment. Negative disables wrapping |

## Authentication (remote providers)

The key resolves as `-api-key`, then `-api-key-env`, then the first token
found under `TARS_API_KEY`, `OPENAI_API_KEY`, `OPENROUTER_API_KEY`,
`DEEPSEEK_API_KEY`, `GROQ_API_KEY`, `TOGETHER_API_KEY`, `ANTHROPIC_API_KEY`.

| Flag | Default | Meaning |
|---|---|---|
| `-api-key KEY` | — | Bearer token. Prefer env vars: a flag lands in shell history |
| `-api-key-env NAME` | — | Env var holding the bearer token, e.g. `OPENROUTER_API_KEY` |

## Task and modes

| Flag | Default | Meaning |
|---|---|---|
| `-workspace DIR` | `.` | Workspace root the agent may read and write |
| `-mission` | `false` | Plan first (human-approved), then one fresh-context worker per subtask, each verified mechanically. Auto-enabled when the task names two or more deliverable files; `-direct` forces the reactive loop instead |
| `-direct` | `false` | Force the reactive loop even when the task looks multi-file |
| `-plan` | `false` | Plan mode: read-only tools, proposes a plan and changes nothing |
| `-yes` | `false` | Skip the mission plan approval gate |
| `-audit PATH` | - | Append gate decisions as JSONL to PATH (off when empty) |
| `-verify-cmd CMD` | — | Command run at the self-check checkpoint, e.g. `"go test ./..."`. Real output is fed back as fact instead of trusting the model's claim |
| `-resume PATH` | — | Continue an interrupted run from its saved state |
| `-fork PATH` | — | Branch from a prior transcript file but write to a fresh task id |
| `-answer TEXT` | — | Answer to supply with `-resume` when the run paused on `ask_user` |
| `-revert` | `false` | Restore tracked workspace files to git HEAD and exit (untracked files kept) |

## Safety and permissions

Policy defaults to allow-all with sensitive reads (`*.env`, credentials,
secrets) gated to ask. See `SECURITY.md` for the trust boundary.

| Flag | Default | Meaning |
|---|---|---|
| `-allow RULES` | — | Comma-separated `tool=pattern` rules to allow, e.g. `"run_shell=go *,read_file=*.go"`. Wins over defaults |
| `-deny RULES` | — | Comma-separated `tool=pattern` rules to deny, e.g. `"run_shell=rm *,read_file=.env"`. Wins over `-allow` |
| `-pure` | `false` | Ignore project config for permissions; built-in defaults plus `-allow`/`-deny` only |
| `-mcp SERVERS` | - | MCP servers as `"name=cmd args...;name2=https://host/mcp"` over stdio JSON-RPC or Streamable HTTP. Tools appear as `mcp__name__tool`. A server that fails to start is skipped with a warning |

## Output and debugging

| Flag | Default | Meaning |
|---|---|---|
| `-mode MODE` | `print` | `print` (human-readable) or `json` (one JSON object per line on stdout; human chatter goes to stderr so the stream pipes cleanly) |
| `-serve` | `false` | Serve JSON-RPC over stdio instead of running one task: methods `run`/`respond`/`cancel`, events on stdout. See `docs/rpc.md` |
| `-tui` | `false` | Fullscreen terminal UI instead of print mode: live transcript, status bar, inline gate prompts. Fresh direct tasks only (no `-plan`, `-resume`, `-fork`, `-mission` with it) |
| `-stream` | `false` | Stream response tokens live. OpenAI/llama backends only; koboldcpp falls back to unary |
| `-debug` | `false` | Log raw request/response JSON plus per-call token usage to `agent-debug.log` |
| `-log-max N` | `300` | Max characters per line in step console output. `0` means no limit. The final answer always prints whole |
