---
name: go
description: Go conventions for this agent. Read before writing or editing .go files.
---

# Go

## Before finishing any Go change
1. `run_shell`: `gofmt -l .` — must print nothing. If it prints files,
   run `gofmt -w .` then check again.
   On Windows, do not mistake line-ending normalization for a code change;
   CI checks formatting on its Linux checkout.
2. `run_shell`: `go build ./...` — must succeed.
3. `run_shell`: `go vet ./...` — must be clean.
4. `run_shell`: `go test ./...` — must pass. If the change has no test
   covering the behavior, add a useful regression test. Skip tests that only
   repeat a trivial implementation.

## Style
- Small, single-purpose packages. No `Manager`/`Kit`/`Helper` suffixes —
  name the thing for what it does.
- Wrap errors: `fmt.Errorf("...: %w", err)`. Never swallow silently.
- Prefer composition over generics. Don't reach for generics unless the
  alternative is real duplicated logic across concrete types.
- No package-level mutable state. Pass dependencies explicitly (struct
  fields / constructor args).
- Keep files short enough to fit in one `read_file` call.

## Editing
- Small fix: `patch_file` or `patch_lines`. New file or full rewrite:
  `write_file`. Don't hand-edit Go via sed/awk in `run_shell`.
