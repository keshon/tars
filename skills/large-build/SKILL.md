---
name: large-build
description: Read before generating anything large (a game engine, a full app, 200+ lines). Check this before a big write_file.
---

# Large builds

A single huge generation is fragile: it can hit the token budget mid-file
(truncated, invalid JSON/code) and the model loses coherence over very
long completions even when it doesn't truncate. Build incrementally
instead:

1. `write_file` a skeleton first — structure, function/class signatures,
   comments, `// TODO` markers. No full implementation yet.
2. Confirm it saved (re-read or list it).
3. Fill in each piece with separate `patch_file`/`patch_lines` calls —
   one feature/module per call, not the whole thing at once.
4. If the plan has explicit phases (like a PLAN.md with Phase 1, 2, 3...),
   actually stop and use separate tool-call steps per phase. Don't let a
   "here's my plan" response turn into one giant generation that tries to
   do every phase at once.

Delegation does NOT help here. `delegate_task` is for independent,
separate pieces that benefit from fresh context (N unrelated files). A single cohesive file (an engine,
a tightly-coupled module) is not parallelizable — building it is
inherently sequential, so do it yourself, incrementally, in this same
conversation.

When a task naturally splits into independent pieces, use one delegate_task
call per piece when fresh context helps. Delegates execute sequentially
because they share the workspace. If a subtask needs a specific expertise
or mindset, use its role field. Explicit human instructions take precedence
over delegation heuristics.
