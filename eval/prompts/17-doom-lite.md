# 17 — doom-lite (the motivating stress case)

Automated run from the repository root: `go run ./cmd/eval -only 17 -runs 1`.
The runner supplies a throwaway workspace and any declared Git seed or replies.

**What it stresses:** everything at once — this is the task shape that
made the reactive loop collapse (loops, broken code, lost goals) and
motivated mission mode. Not expected to pass every run on a 12–25B
model; the interesting measurement is *how it fails* and how far it
gets compared to direct mode on the same prompt.

## Run

```bash
mkdir -p sandbox/doom-lite

go run ./cmd/agent -mission -workspace sandbox/doom-lite \
  "create a first-person maze game in plain HTML/JS (no build tools, no libraries): index.html with a canvas, a raycasting renderer that draws walls of a hardcoded maze map in walls.js, WASD movement with collision in player.js, and a main game loop in game.js. The player walks around the maze in first person."
```

At the gate, expect 4–7 subtasks along the lines of: canvas scaffold →
maze map data → raycasting renderer → movement/collision → integration.
Reject plans that put everything in one subtask (that's the reactive
loop with extra steps) or that invent a dev server/build tool.

For an A/B comparison, run the same prompt without `-mission` and diff
step counts, produced files, and whether the result renders anything.

## Judge

| Signal | Usually means |
|--------|----------------|
| every subtask worker starts under ~5k prompt tokens | context compiler doing its job — check `workers/*.json` |
| worker N re-implements worker N-1's file from scratch | scope discipline breaking; check whether the seed's mutated-files list included it |
| renderer subtask fails check → fix worker converges | the loop working on genuinely hard content |
| replan produces a meaningfully different decomposition | best case for a weak model |
| mission FAILED with facts after budget exhaustion | acceptable outcome — read the ledger, that's the data |
| any subtask attempted >3 times within one plan or >1 replan | BUG — budgets must bound by construction |

**Pass (utopian)** = mission DONE and index.html shows a moving
first-person view. **Pass (realistic)** = ≥3 subtasks done with passing
checks, all files non-empty and referencing each other correctly, and a
final report whose facts accurately describe whatever state it reached.

Save the ledger (`mission.json`) from each run — comparing decompositions
across runs shows whether plan quality or worker quality is the current
bottleneck, which decides where the next engineering effort goes.

The automated probe checks canvas/script references and script existence. It
does not prove rendering, collision, or playability; perform those browser
checks separately. A failed mission is scored as failure even if its partial
outputs are useful to inspect.
