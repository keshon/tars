# 14 — Multi-file completion (legacy filename: parallel)

Automated run from the repository root: `go run ./cmd/eval -only 14 -runs 1`.
The runner supplies a throwaway workspace and any declared Git seed or replies.
For the manual examples below, first copy `eval/fixtures` into
`sandbox/eval-manual`; use a fresh copy for each scenario.


**Stresses:** completion of all three independent file requests within the frozen step budget.

**Run:**
```bash
go run ./cmd/agent -log-max 300 -workspace sandbox/eval-manual \
  "create alpha.txt with content alpha, beta.txt with content beta, and gamma.txt with content gamma"
```

**Pass (strict):**
- Three files exist with correct content
- Direct file writes are enough; delegation is not required. File writes and
  delegates execute through ordered exclusive barriers, not parallel mutation.

**Automated pass:**
- Three files correct in ≤8 steps, any tool pattern

**Fail:**
- Only 1–2 files created, claims all three
- More than eight scored steps
- Three `delegate_task` spawning runaway subagents (>12 steps each)

**Cleanup:** `rm -f sandbox/eval-manual/alpha.txt sandbox/eval-manual/beta.txt sandbox/eval-manual/gamma.txt`
