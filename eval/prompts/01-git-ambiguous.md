# 01 — Git ambiguous wording

Automated run from the repository root: `go run ./cmd/eval -only 01 -runs 1`.
The runner supplies a throwaway workspace and any declared Git seed or replies.

**Stresses:** literal ".git" path, `git log` tweak loops, `ls` instead of `list_files`.

**Run:**
```bash
go run ./cmd/agent -log-max 300 \
  "scan .git dir and form a list of all commits in a readable form"
```

**Pass (weak model realistic):**
- ≤5 steps
- `git log` from repo root (no `.git` path argument)
- No `run_shell` with `ls`/`dir` for exploration
- Correct commit list in final answer

**Fail:**
- `git log … .git` or `git log -- .git`
- ≥6 steps of shell tweaking
- Declares success without showing real `git log` output

**Notes:** Deliberately evil phrasing — same task that burned 9 steps before the git skill.
