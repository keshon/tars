# Eval seed workspace

This directory is copied into a throwaway workspace for each automated probe.
Its source intentionally starts before the requested changes:

- `sample.go`: Version is `v1`; Greet returns `"hello"`; Bye is absent.
- `hello.txt`: greeting and rename target for probe 04.
- `dup.txt`: duplicate patch markers for probe 06.
- `big.txt`: fixed 205,264-byte text fixture for probe 10.
- This README: fixture context for read-only and clarification scenarios.

Probes test changes such as Version `v2`, Greet returning `"hi"`, and adding
Bye returning `"bye"`. Those are desired outputs, not the seed's current state.
Use `go run ./cmd/eval` from the repository root; do not edit these seed files
to make a probe pass. See [the eval guide](../README.md).
