# 19 — Snapshot revert

Automated run from the repository root: `go run ./cmd/eval -only 19 -runs 1`.
The runner supplies a throwaway workspace and any declared Git seed or replies.

**Stresses:** the harness snapshot path (`snapshot.Track`/`Revert`), not the
model. The eval runner git-initializes the throwaway workspace, captures the
pre-run diff, and after scoring asserts the tree is restored exactly.

**Run:**
```bash
go run ./cmd/eval -only 19
```

**Pass:**
- `revert-me.txt` contains `eval-snapshot` (the agent did the work —
  scored before the revert, against the modified tree)
- `write_file` was called
- After the run: the file is gone and every seed file is byte-identical
  (the revert assertion — scored after, against the restored tree)

**Fail:**
- Agent never wrote the file (empty diff — revert would prove nothing,
  so the probe fails rather than passing vacuously)
- Seed files lost, changed, or agent files surviving the revert

**Note:** production `snapshot.Revert` restores pre-existing untracked files
and removes newly created nonignored files. Ignored paths, `.tars` state and
Git history are excluded. The eval tail uses the production restore directly;
there is no extra `git clean` hiding incomplete rollback.
