# 04 — Rename via move_file

Automated run from the repository root: `go run ./cmd/eval -only 04 -runs 1`.
The runner supplies a throwaway workspace and any declared Git seed or replies.
For the manual examples below, first copy `eval/fixtures` into
`sandbox/eval-manual`; use a fresh copy for each scenario.


**Stresses:** atomic rename — not `mv`/`ren` in shell, not read+write.

**Run:**
```bash
go run ./cmd/agent -log-max 300 -workspace sandbox/eval-manual \
  "rename hello.txt to goodbye.txt"
```

**Pass:**
- Single `move_file` from `hello.txt` to `goodbye.txt`
- `goodbye.txt` exists with original content; `hello.txt` gone
- ≤4 steps

**Fail:**
- `run_shell` with `mv`, `ren`, `cp`, `rm`
- `read_file` + `write_file` + delete pattern
- Empty placeholder file

**Reset fixture:**
```bash
mv sandbox/eval-manual/goodbye.txt sandbox/eval-manual/hello.txt 2>/dev/null || true
```
