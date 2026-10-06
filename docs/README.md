# Documentation

Current guides:

- [Quick start](../README.md): requirements, terminal/server launch, saved runs.
- [CLI reference](cli.md): flags, backend examples, permissions, mode limitations.
- [Terminal guide](tui.md): composer, suggestions, file references, sessions and keys.
- [RPC protocol](rpc.md): stdio requests, events, gates and client lifetime.
- [Architecture](../ARCHITECTURE.md): packages, loop, role budgets and frontends.
- [Security](../SECURITY.md): trust boundary, approvals, files and network behavior.
- [Conventions](conventions.md): enforced checks and coding practices.
- [Eval guide](../eval/README.md): frozen probes and repeatable scoring.
- [Agent skills](../skills/README.md): task-workspace guidance for the local model.
- [Manual example task](example-task.md): landing-page acceptance criteria.

Historical material:

- [Audit and implementation follow-up](audit-2026-10-06.md): reviewed revisions
  and their evidence; findings are not an automatically maintained issue list.
- [Early audit overview](../TARS_AUDIT.md): unversioned background material.
- `reference/`: captured research, UI inspirations and working notes, not the
  current product specification.

The code is authoritative for behavior. Documentation checks cover registered
CLI flags, referenced identifiers and convention tags; they do not prove that
an example works or that every description remains semantically current.
