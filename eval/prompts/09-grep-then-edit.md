# 09 — Grep then edit

Automated run from the repository root: `go run ./cmd/eval -only 09 -runs 1`.
The runner supplies a throwaway workspace and any declared Git seed or replies.
For the manual examples below, first copy `eval/fixtures` into
`sandbox/eval-manual`; use a fresh copy for each scenario.


**Stresses:** `grep_files` before read; avoid reading every file.

**Run:**
```bash
go run ./cmd/agent -log-max 300 -workspace sandbox/eval-manual \
  "find which file defines Greet and change it to return \"hi\" instead of \"hello\""
```

**Pass:**
- `grep_files` for `Greet` (or `func Greet`) before editing
- One surgical edit in `sample.go`
- Not 5+ `read_file` on unrelated files

**Fail:**
- `list_files` + `read_file` everything in fixtures
- Shell `grep` instead of `grep_files`
- ≥8 exploratory steps (SearchFatigue territory)
