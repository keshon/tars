# Security Policy

TARS runs locally within the security boundary of the user running it.
It is the operator's responsibility to monitor it or contain it in a
container or VM.

TARS treats the local user account and files writable by that account as
inside the same trust boundary as the agent process. `internal/workspace`
bounds file tools against mistyped paths, not attackers: `run_shell` sets a
working directory and nothing else, symlinks are followed, and child
processes inherit a scrubbed environment (no `*_API_KEY`/`*_SECRET`).

TARS relies on trustworthy skills, extensions and repositories. Files like
`SKILL.md`, `AGENTS.md` or comment instructions can prompt-inject the agent
and this cannot be protected against — only contained with approvals and
isolation.

## Out of scope

- Local code execution or sandboxing (intentionally no sandbox).
- Prompt injection via repo files, skills, tool output, web content.
- Untrusted skills, extensions, MCP servers, repositories.
- User-approved or user-initiated local actions.
- Exposed third-party/user-controlled credentials.
- Reports requiring prior local write access (home dir, workspace, env,
  shell startup, TARS config) unless they show how TARS granted it.
- Resource exhaustion requiring trusted local input.

Report genuine boundary bypasses (privilege escalation, escaping the
workspace guard via the harness itself) with repro, commit and impact.
