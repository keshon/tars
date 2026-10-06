# 18 — Overflow recovery

Automated run from the repository root: `go run ./cmd/eval -only 18 -runs 1`.
The runner supplies a throwaway workspace and any declared Git seed or replies.

**Stresses:** the `IsOverflow` recovery path in `agent.run` — compact and
continue instead of dying on a context-window error.

**Setup:** the eval runner fails the run's first model call with a synthetic
`APIError{Overflow: true}` (`inject_overflow_once`), then passes through to
the live backend. The injected error is indistinguishable from a backend
400-context-exceeded, so the run exercises the real recovery code. No live
backend produces an overflow on demand, which is why this is injected
rather than observed.

**Run:**
```bash
go run ./cmd/eval -only 18
```

**Pass:**
- Run completes despite the step-0 overflow (without recovery it dies with
  a backend failure before doing anything)
- Uses `read_file`, answers `Hello from eval fixtures`
- No `write_file`

**Fail:**
- Run errors on step 0 (recovery missing or broken)
- Answer wrong or read never happened (recovery corrupted the run)
