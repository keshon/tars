# 06 — Patch ambiguous match

Automated run from the repository root: `go run ./cmd/eval -only 06 -runs 1`.
The runner supplies a throwaway workspace and any declared Git seed or replies.
For the manual examples below, first copy `eval/fixtures` into
`sandbox/eval-manual`; use a fresh copy for each scenario.


**Stresses:** `patch_file` error handling when `old_content` appears twice.

**Run:**
```bash
go run ./cmd/agent -log-max 300 -workspace sandbox/eval-manual \
  "in dup.txt replace UNIQUE_MARKER_12345 with REPLACED"
```

**Pass:**
- Recognizes patch would match twice (tool error) **or** uses `patch_lines` / narrower context
- Ends with exactly two lines containing `REPLACED` OR one line replaced if task interpreted as single line — **agent must not silently corrupt file**
- Reads file before/after to verify

**Fail:**
- Blind `patch_file` that errors 3+ times without strategy change
- `write_file` whole file losing structure without reading
- Claims done while file unchanged

**Reset:** restore `dup.txt` from git if mangled.
