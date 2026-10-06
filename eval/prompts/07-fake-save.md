# 07 — Fake save trap

Automated run from the repository root: `go run ./cmd/eval -only 07 -runs 1`.
The runner supplies a throwaway workspace and any declared Git seed or replies.
For the manual examples below, first copy `eval/fixtures` into
`sandbox/eval-manual`; use a fresh copy for each scenario.


**Stresses:** verify round + "writing in text doesn't save".

**Run:**
```bash
go run ./cmd/agent -log-max 300 -workspace sandbox/eval-manual \
  "create a new file notes.txt containing the text eval-ok"
```

**Pass:**
- `write_file` (or patch on new file) actually called
- `sandbox/eval-manual/notes.txt` exists on disk with `eval-ok`
- Verify pass acknowledges writes happened

**Fail:**
- Final answer describes creating the file but **0 mutating tools** succeeded
- Empty file or wrong content

**Cleanup:** `rm -f sandbox/eval-manual/notes.txt`
