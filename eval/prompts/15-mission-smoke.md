# 15 — mission mode smoke test

Automated run from the repository root: `go run ./cmd/eval -only 15 -runs 1`.
The runner supplies a throwaway workspace and any declared Git seed or replies.

**What it stresses:** the whole mission pipeline on a task that is easy
per-subtask but multi-file: grammar-constrained plan generation, the
approval gate, fresh-context workers, mechanical checks, the final
verify pass. This is also the **live gate for the grammar bet** — run it
with `-debug` the first time and confirm in `agent-debug.log` that the
plan request carries structured-output constraints (GBNF on KoboldCPP,
JSON schema on llama/OpenAI) and the response is plan JSON,
not prose.

## Run

```bash
# scratch workspace so the site files don't land in the repo
mkdir -p sandbox/mission-smoke

go run ./cmd/agent -mission -debug -workspace sandbox/mission-smoke \
  "create a small static site: index.html with a button and a counter, style.css with basic styling, script.js that increments the counter on click"
```

At the approval gate, read the plan before typing `y`:
- 2–4 subtasks, each with a `file_exists` (or honest `shell`) check?
- goals self-contained (understandable without the other subtasks)?
- no invented build tools (no npm/vite for a static site)?

Try typing a revision note instead of `y` once, to exercise the
regenerate path. Use `-yes` on repeat runs.

## Judge

| Signal | Usually means |
|--------|----------------|
| plan call returns prose or broken JSON | grammar not honored by backend — check `-debug` log, consider the `{m,n}` bounds fallback noted in internal/mission/grammar.go |
| plan has `echo`-style checks | validator gap — should have been rejected before you saw it |
| worker for s2 rewrites s1's files from scratch | scope discipline failing; check the seed's ledger section in the worker state file |
| `[mission] subtask sN: check FAILED` on work that looks done | check declared by planner doesn't match what the task built — plan quality issue, note the shape |
| mission FAILED with facts intact | working as designed — read the report, that's the point |

**Pass** = mission reaches DONE, all three files exist with real content,
the button actually increments (open index.html), and each worker ran in
a fresh context (worker state files in `sandbox/mission-smoke/.tars/tasks/<id>/workers/` each
start from the compiled seed, not from another worker's transcript).

Also worth one Ctrl+C mid-execute followed by
`go run ./cmd/agent -workspace sandbox/mission-smoke -resume sandbox/mission-smoke/.tars/tasks/<id>` to confirm the mission
resumes at the interrupted subtask instead of restarting.

The automated probe checks file references and click-handler content; a passing
score does not replace the manual browser check that the button increments.
