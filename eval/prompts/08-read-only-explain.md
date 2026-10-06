# 08 — Read-only explain

Automated run from the repository root: `go run ./cmd/eval -only 08 -runs 1`.
The runner supplies a throwaway workspace and any declared Git seed or replies.
For the manual examples below, first copy `eval/fixtures` into
`sandbox/eval-manual`; use a fresh copy for each scenario.


**Stresses:** read-only task must finish **without** spurious file writes.

**Run:**
```bash
go run ./cmd/agent -log-max 300 -workspace sandbox/eval-manual \
  "explain what sample.go does in 2-3 sentences. do not modify any files"
```

**Pass:**
- Uses `read_file` (maybe `list_files` first)
- **No** `write_file` / `patch_*` / `move_file`
- Accurate short explanation of `sample.go`
- Finishes after verify without inventing changes

**Fail:**
- Any mutating tool call
- Explanation without reading the file
- Refuses to finish because "0 writes" nudge confused it
