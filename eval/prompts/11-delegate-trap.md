# 11 - Delegate restraint

Automated run from the repository root: `go run ./cmd/eval -only 11 -runs 1`.
The runner supplies a throwaway workspace and any declared Git seed or replies.

Tests whether a small, cohesive edit stays in the current context.

Task: add `Bye() string` returning `"bye"` to `sample.go`.

Pass: edit the file directly, preserve `Greet`, and produce valid Go.
Fail: delegate the tiny edit, duplicate the work, or damage the file.

Explicit user requests to delegate take precedence over this default. The
probe does not ask for delegation; it measures the agent's own judgment.
