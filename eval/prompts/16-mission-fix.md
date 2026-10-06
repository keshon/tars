# 16 — mission fix loop

Automated run from the repository root: `go run ./cmd/eval -only 16 -runs 1`.
The runner supplies a throwaway workspace and any declared Git seed or replies.

**What it stresses:** the bounded fix loop and, if it doesn't converge,
the replan path. The task deliberately includes a check a weak model's
first attempt tends to fail (a real command with a strict exit code, not
`file_exists`), so the interesting part is what happens *after* the first
`check FAILED`.

## Run

```bash
mkdir -p sandbox/mission-fix

go run ./cmd/agent -mission -workspace sandbox/mission-fix \
  "write a Python script stats.py that reads numbers.txt (one integer per line) and prints their sum, then create numbers.txt with the numbers 3, 5 and 34"
```

Steer at the approval gate: the plan should end with a `shell` check like
`python stats.py` (exit 0) — if the planner picked something vacuous the
validator should already have rejected it; if it picked `file_exists`
only, type a revision note asking for a real run as the final check.

## Judge

| Signal | Usually means |
|--------|----------------|
| `check FAILED — starting fix worker (1/2)` then `check PASSED` | the design working: real error output → focused fix |
| fix worker rewrites everything from scratch | "do not redesign" rule ignored — note it; the seed carried the failing output, check whether the model used it |
| both fix attempts fail → `replanning (1/1)` | acceptable; read whether the new plan actually changes approach or repeats it |
| replan repeats the failed approach verbatim | weak-model ceiling; the budget still bounds it — mission fails loudly after 1 replan |
| same subtask attempted more than 3 times within one plan | BUG — the attempt budget must bound this by construction |

**Pass** = mission reaches DONE with `python stats.py` printing 42, in at
most 1 + 2 fix attempts per subtask for each plan and ≤1 replan. A failed
mission with an accurate report is useful diagnostic evidence, but is not a
passing automated score. Probe 16 is Windows-only because its final shell
assertion uses `findstr`; it also requires Python on PATH.

Inspect `sandbox/mission-fix/.tars/tasks/<id>/mission.json` afterwards: each attempt must
have its own `attempt N wrote:` / `check:` fact pair, and each fix worker
its own `workers/sN-aK.json` transcript starting from a fix seed (not a
continuation of the failed attempt's conversation).
