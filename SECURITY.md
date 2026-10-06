# Security Policy

TARS runs locally within the security boundary of the user running it.
It is the operator's responsibility to monitor it or contain it in a
container or VM.

TARS treats the local user account and files writable by that account as
inside the same trust boundary as the agent process. Deny rules are terminal;
Ask rules fail closed when no approval handler exists. Approvals marked
Always last only for the current run. `internal/workspace`
bounds file tools against mistyped paths, not attackers: `run_shell` sets a
working directory and nothing else, symlinks are followed, and child
processes inherit a scrubbed environment (no `*_API_KEY`/`*_SECRET`).

TARS relies on trustworthy skills, extensions and repositories. Files like
`SKILL.md`, `AGENTS.md` or comment instructions can prompt-inject the agent
and this cannot be protected against — only contained with approvals and
isolation.

## User attachments and approvals

Model tool calls go through policy; explicit user attachments are a separate
path. TUI `@path` references do not invoke read_file approval or sensitive-path
filtering. They intentionally send the selected file to the configured model
backend, which may be hosted. TUI references must remain inside the workspace
after symlink resolution and satisfy the documented text/image limits. CLI/RPC
image paths use the workspace's textual guard, like the file tools.

The default policy asks for sensitive reads and known destructive shell or
background command shapes. Explicit Deny cannot be approved away. CLI/RPC
`-yes` auto-approves mission plans but denies permission Ask requests;
it does not authorize every tool. TUI permission requests remain interactive.
All frontends remember an Always approval for the exact tool/resource pair
for the current run only. These grants are not saved with the session.

## Network probes

`check_url`, `webfetch`, `fetch_raw`, and `search_web` use GET requests and send no credentials: no
`Authorization`, `Cookie`, or `Proxy-Authorization` header leaves the
process (asserted by `TestHealthProbes_SendNoCredentials`), and the child
environment is scrubbed before any shell runs. `webfetch` and `fetch_raw` refuse
non-public URLs before dialing (loopback, intranet names, non-global IPs
including legacy `inet_aton` spellings). It validates redirects and every
resolved address, then connects to a validated literal IP to prevent DNS
rebinding. Web search uses the same public-destination transport.
Environment proxies are disabled. `check_url` permits loopback
because probing a just-started dev server is its job, and returns only a
status plus a 512-byte prefix. A health probe must never become a
credential or intranet oracle: any new network tool keeps these rules.

Repository search follows Git ignore rules (tracked files remain searchable)
and excludes sensitive paths; explicit reads use the permission gate. Shell
commands can still read those files under the local account. Shell tools and
mission checks share environment scrubbing, bounded output, cancellation,
and process-tree cleanup. Model-generated mission checks also pass policy
and approval gates. MCP servers are trusted local extensions.

State, debug traces and checkpoint archives can contain repository content
and tool output. They are local plaintext artifacts; protect and remove them
according to the sensitivity of the workspace. Session logs rotate at 16 MiB,
but checkpoints and optional debug traces have no automatic retention policy.

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
