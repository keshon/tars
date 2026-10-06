# 10 — Large read truncation

Automated run from the repository root: `go run ./cmd/eval -only 10 -runs 1`.
The runner supplies a throwaway workspace and any declared Git seed or replies.
For the manual examples below, first copy `eval/fixtures` into
`sandbox/eval-manual`; use a fresh copy for each scenario.


**Stresses:** `read_file` metadata vs body — must not poison context on size-only questions.

**Fixture:** `eval/fixtures/big.txt` is committed and is exactly 205,264 bytes.
Copy it unchanged for manual runs; do not regenerate random content, because
probe 10 asserts the fixed size. It is text containing base64-like noise.

**Run:**
```bash
go run ./cmd/agent -log-max 300 -workspace sandbox/eval-manual \
  "read big.txt and tell me the exact file size in bytes and whether it looks like text"
```

**Pass:**
- Uses `read_file` with `metadata_only` or `max_bytes: 0` (ideal), **or** reads once and cites FILE header fields (`size:`, `truncated:`, `binary:`)
- Reports exactly 205,264 bytes and distinguishes text encoding from meaningful prose
- Run completes in ≤5 steps without context blow-up or derailed verify

**Fail:**
- Full `read_file` dumps 128KB+ into history and run derails (mojibake, confused verify, wanted `ask_user` but couldn't)
- Claims to have read entire file with no truncation awareness
- Repeated `read_file` loops

**Cleanup:** discard only the manual scratch copy; retain the committed fixture.
