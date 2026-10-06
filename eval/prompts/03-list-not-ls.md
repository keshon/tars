# 03 — List directory, not ls

Automated run from the repository root: `go run ./cmd/eval -only 03 -runs 1`.
The runner supplies a throwaway workspace and any declared Git seed or replies.
For the manual examples below, first copy `eval/fixtures` into
`sandbox/eval-manual`; use a fresh copy for each scenario.


**Stresses:** `list_files` discipline vs `run_shell` + ls.

**Run:**
```bash
go run ./cmd/agent -log-max 300 -workspace sandbox/eval-manual \
  "what files are in the workspace root? just list their names"
```

**Pass:**
- Uses `list_files` (path `.` or empty)
- Does **not** call `run_shell` with `ls`, `dir`, or `find`
- Correct seed names: `README.md`, `hello.txt`, `sample.go`, `dup.txt`, `big.txt`

**Fail:**
- `run_shell: ls` / `dir`
- Invents filenames without listing
