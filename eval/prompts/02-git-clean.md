# 02 — Git clean (control)

Automated run from the repository root: `go run ./cmd/eval -only 02 -runs 1`.
The runner supplies a throwaway workspace and any declared Git seed or replies.

**Stresses:** baseline — should match the good run in `a6bf9516`.

**Run:**
```bash
go run ./cmd/agent -log-max 300 \
  "выведи git log --oneline из корня репозитория"
```

**Pass:**
- 1× `run_shell` with `git log --oneline` (or equivalent)
- ≤3 model round-trips including verify
- Output matches real history

**Fail:**
- Extra exploration steps
- Wrong directory or path arguments

**Notes:** Regression guard after `skills/git` + system prompt changes.
