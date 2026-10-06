---
name: dev-server
description: Read before testing anything that runs a local server (npm run dev, a Go http server, etc).
---

# Testing a dev server

A server process never exits on its own — that's the whole point of it.
Treat it differently from a normal command:

1. `start_background` with the actual run command (`npm run dev`, `go run .`,
   etc). Never `run_shell` for this — it blocks until the command exits,
   which a server never does, so run_shell will just time out and kill it.
2. Wait a couple seconds, then `check_url` against the URL it's supposed
   to serve. A startup banner saying "Local: http://localhost:5173/" is
   not proof it's reachable — `check_url` is.
3. If `check_url` fails: `check_background` to read the actual stdout/
   stderr captured so far. Real errors (port already in use, missing
   dependency, syntax error) show up there. Diagnose from that, not from
   guessing.
4. Fix the real cause, then repeat from step 1 (start a new background
   process — the old one may need `stop_background` first if it's still
   holding the port).
5. Use `stop_background` when checks no longer need the server. Managed
   background processes are cleaned up when the frontend exits; do not promise
   that a server will survive TARS closing. For a persistent server, give the
   user the startup command to run in a separate terminal.
