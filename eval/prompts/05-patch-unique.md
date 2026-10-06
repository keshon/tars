# 05 — Patch unique match

Automated run from the repository root: `go run ./cmd/eval -only 05 -runs 1`.
The runner supplies a throwaway workspace and any declared Git seed or replies.
For the manual examples below, first copy `eval/fixtures` into
`sandbox/eval-manual`; use a fresh copy for each scenario.


**Stresses:** `patch_file` on a small change.

**Run:**
```bash
go run ./cmd/agent -log-max 300 -workspace sandbox/eval-manual \
  "in sample.go change Version from v1 to v2"
```

**Pass:**
- `patch_file` or `patch_lines` (not full `write_file` rewrite of whole file)
- `sample.go` contains `const Version = "v2"`
- `go build` would succeed (file still valid Go)

**Fail:**
- Rewrites entire file via one giant `write_file`
- Changes only in response text, not on disk
- Breaks syntax
