# 19 — Snapshot revert

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

**Note:** production `snapshot.Revert` keeps untracked files (a
model-created file must never be silently deleted). The eval workspace is
throwaway, so the probe additionally runs `git clean -fd` — exact tree
equality needs untracked output gone too.
