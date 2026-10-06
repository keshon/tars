# skills/

Lean, checklist-style guides for THIS agent — not general documentation
copied from elsewhere. Written for a small local model: short, directive,
no prose padding, no theory.

Convention: `skills/<name>/SKILL.md`. Discovered via `list_files` +
`read_file` — no special tool needed (see system prompt). Skills must exist
in the task workspace: launching TARS from this repository does not install
these guides into a different workspace. Copy the relevant skills there if
you want the agent to use them. Project/ancestor AGENTS.md instructions load
automatically; SKILL.md is read by the model when relevant.

Baseline tool discipline lives in the system prompt. Skills cover
task-specific workflows (git, dev servers, large builds, language quirks).

Rules for writing one:
- Lead with a checklist, not an essay.
- Name this agent's actual tools (`patch_file`, `move_file`, `run_shell`,
  `delegate_task`) — generic advice is dead weight here.
- Keep it under ~100 lines. Longer means split it.

Current guides: [Git](git/SKILL.md), [Go](go/SKILL.md),
[TypeScript](typescript/SKILL.md), [dev servers](dev-server/SKILL.md),
and [large builds](large-build/SKILL.md). Read-only roles cannot follow steps
requiring shell commands or writes; their tool sets enforce that limit.
