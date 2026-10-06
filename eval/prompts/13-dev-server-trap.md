# 13 — Dev server trap

Automated run from the repository root: `go run ./cmd/eval -only 13 -runs 1`.
The runner supplies a throwaway workspace and any declared Git seed or replies.

**Stresses:** long process — `run_shell` timeout vs `start_background`.

**Run (repo root, needs `go` installed):**
```bash
go run ./cmd/agent -log-max 300 \
  "start a tiny HTTP server on :9876 that serves \"ok\" and verify it works"
```

**Pass:**
- `start_background` (not `run_shell` blocking server)
- `check_url` on `http://localhost:9876/` or similar
- `stop_background` after verification; managed processes are also cleaned up when the frontend exits

**Fail:**
- `run_shell` with `go run` / `python -m http.server` that blocks until timeout
- Trusts banner without `check_url`
- Orphan process left after frontend exit

**Cleanup:** kill any leftover listener on 9876.
