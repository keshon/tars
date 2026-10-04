# RPC over stdio (`-serve`)

`agent -serve` speaks line-delimited JSON-RPC on stdin and writes event
lines plus id-responses on stdout. Human chatter always goes to stderr,
so stdout parses cleanly. Single-flight: one run and one pending gate at
a time; anything else reports an error instead of queueing.

## Methods

```json
{"id": 1, "method": "run", "params": {"task": "add Bye() to sample.go", "mission": false}}
{"id": 2, "method": "respond", "params": {"answer": "y"}}
{"id": 3, "method": "cancel"}
```

- `run` starts a direct run (`"mission": true` runs the planner
  pipeline instead). Each run gets a fresh task dir and snapshot, like
  the CLI. Answers arrive as `{"id": 1, "result": {"answer": "..."}}`
  or `{"id": 1, "error": {"message": "..."}}`.
- `respond` answers the currently suspended gate with raw text. The
  y/a/n and approve/reject mappings live server-side, exactly as on
  the CLI: reply `"y"` to allow, anything else to deny; reply `"y"` to
  approve a plan, `"n"` to reject, anything else as a revision note.
- `cancel` aborts the in-flight run. A second `run` while one is active
  is refused; `respond` with no gate pending is refused.

Malformed lines answer `{"id": null, "error": ...}`. Stdin EOF drains
the in-flight run before exiting (a piped one-shot closes stdin right
after its request), unless the run is suspended on a gate — its answer
was going to arrive on the stdin that just closed, so the run is
cancelled instead of hanging. Ctrl+C aborts immediately. Human chatter
always goes to stderr in serve mode, whatever `-mode` says.

## Events (no id)

The same vocabulary as `-mode json`, so one parser serves both:

```json
{"seq": 1, "event": "run_start", "task": "...", "mission": false}
{"seq": 2, "event": "step", "label": "", "step": 0, "tool_calls": [{"name": "read_file", "args": "{...}"}]}
{"seq": 3, "event": "tool_result", "call_id": "call_1", "text": "..."}
{"seq": 4, "event": "usage", "step": 0, "prompt": 2100, "completion": 120, "cached": 0}
{"seq": 5, "event": "awaiting_input", "kind": "permission", "id": "", "tool": "read_file", "resource": ".env", "prompt": "[permission] read_file on \".env\""}
{"seq": 6, "event": "input_answered", "kind": "permission", "id": ""}
{"seq": 7, "event": "mission", "text": "subtask s1: check PASSED"}
{"seq": 8, "event": "result", "answer": "..."}
{"seq": 9, "event": "finding", "scope": "per-edit", "rule": "gofmt", "path": "a.go", "line": 3, "summary": "not gofmt-clean"}
{"seq": 10, "event": "nudge", "kind": "verify", "text": "[harness] ..."}
{"seq": 11, "event": "delta", "text": "partial answer..."}
```

`kind` is `ask_user`, `permission`, or `plan_approval`. Permission gates
also carry `tool`, `resource`, and the operator-facing `prompt` (ask
gates carry `prompt` only), so a client can name the gated call
without keeping gate state. A `respond` answer of `n: <note>` denies
with the note attached: the refusal the model sees carries the
operator's redirect. Deterministic post-write checks surface as
`finding` events (`scope` per-edit or session-end); they are
report-only, never gates. Loop-generated harness notices surface as
`nudge` events (`kind` verify/refusal/leak/think-wrap/truncated/
overflow/budget/stuck/todo/closing): the same `[harness]`-marked text the model
saw, so observers can audit every intervention. Streamed content
chunks surface as `delta` events (display-only; the step event
carries the authoritative text and replaces whatever was live). All flags that
shape a CLI run (`-backend`, `-workspace`, `-allow`, `-mcp`, `-yes`,
`-reasoning-budget`, …) shape `-serve` identically — it is the same
harness behind a different front door.

## Example

```bash
printf '%s\n' \
  '{"id":1,"method":"run","params":{"task":"what files are here?"}}' |
  go run ./cmd/agent -serve -backend-kind llama -backend http://127.0.0.1:8080 -workspace ./site |
  while IFS= read -r line; do echo "$line" | jq -c '{event, id, result}'; done
```
