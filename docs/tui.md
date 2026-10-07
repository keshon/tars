# Terminal guide

Start TARS with `go run ./cmd/agent -tui`, or use `run-tui.cmd` on Windows.
The UI uses Bubble Tea v2. [CLI flags](cli.md) configure the backend,
workspace, permissions, verification, and response budgets.

## Windows launchers

`run-kobold.cmd` starts KoboldCPP in the current terminal and keeps its output
separate from TARS. The shipped paths are specific to the original machine:

- Executable: `E:\Projects\ai-text\koboldcpp-extracted\koboldcpp-launcher.exe`
- Model: `E:\Projects\ai-text\models\unsloth\Qwen3.5-9B-GGUF\Qwen3.5-9B-Q4_K_M.gguf`

Edit both variables and the server working directory when installing elsewhere.
The script binds `127.0.0.1:5001`, uses CUDA GPU 0, requests GPU offload for
all layers, and sets a 16,384-token context. It detects an existing KoboldCPP
server on that port rather than starting another one.

Once the model is ready, run `run-tui.cmd` in another terminal. It checks
`/api/v1/model`, builds `agent.exe`, and connects with the `kobold` dialect.
Its workspace is this checkout and its response budget is 4,096 tokens.
Extra arguments are forwarded to the binary:

```bat
run-tui.cmd -workspace D:\Projects\my-app -verify-cmd "go test ./..."
```

Keep flags before any task text. Closing TARS leaves the independently
started KoboldCPP server running. Closing the server terminal stops that server.
The optional `run-remote.cmd` and `run-serve.cmd` files are local, gitignored
helpers; they are not required or distributed as part of the setup.

## Screen and focus

The header shows session identity, Plan/Act mode, model, and run status.
During a run, a Braille spinner and step/context/elapsed metrics appear
at the top; the Run menu includes tool and file counts. Estimated context
usage has a `~` prefix.

At 110 columns and 18 rows or larger, the 34-column Sessions pane appears
unless hidden with F6 or Ctrl+B. Tab switches between the pane and input.
Gray rules stay neutral; the focused pane label is colored. A cyan `▌`
marks the current saved session; `›` marks the row selected in the focused
pane. Ages are gray. `*`, `?`, and `!` indicate working/stopping, input
needed, and failed/unreadable states.

The transcript distinguishes YOU, TARS, and SYS/ERR notices. New turns use
extra spacing; horizontal rules are reserved for pane boundaries. Thinking and
tool results can be collapsed or expanded with F3 or Ctrl+G. Collapsing is
visual only; it does not remove model history. Scrolling up stops automatic
following; F7 or Ctrl+End returns to the latest content.

## Header badges

The session title and clock sit above a badge strip: Act/Plan, Ready/Work/Wait/
Stop/Error, model, context, and run progress. Work includes the braille spinner.
Click a badge or use F9 to open its menu without moving the conversation. Pane
rules remain gray. Narrow terminals omit run and model badges before context.

Mode offers Act/Plan while idle; Activity offers Stop or Retry when applicable.
Model shows the configured backend, sanitized endpoint, transport, context
window, and response budget. These are configuration details, not server-health
measurements. Context offers manual compaction when an idle chat has saved state.

The context badge retains the latest request's prompt usage between turns.
`~CTX` marks an estimate. Opening or compacting a saved chat recomputes an
estimate from replayed text and tool calls, excluding tool schemas and image
tokens. Context details identify the source and show previous request usage,
which is saved in optional `usage.json` alongside `state.json`. Missing or
corrupt usage metadata does not prevent opening a session.

Session scans run in background commands, with revision checks preventing stale
results from replacing newer state. The sidebar loads session metadata; opening
Search prepares conversation text. Unchanged histories reuse the session cache.
Opening old history omits message timestamps because the saved format does not
record them; live messages retain their actual display times.

The ownership rules and their regression tests are documented in
[tui-ownership.md](tui-ownership.md).

## Input and suggestions

The composer sits below the transcript inside the conversation pane; its
suggestions and prompts share that pane's width. The Sessions divider extends
to a full-width separator above the global function-key footer. Session
details sit at the bottom of the pane without a separate divider. Hiding Sessions gives the composer the full
terminal width.

The prompt is `>` with an underline caret. The input grows to fit multiline
text, up to six visible rows (less in short terminals).

- Enter sends when idle, or queues one follow-up during a run. Empty Enter
  does nothing.
- Shift+Enter adds a line. Ctrl+O is the fallback.
- Up/Down moves within the draft, unless the suggestion picker is open.
- Alt+Up/Alt+Down recalls submitted input and restores the unsent draft.
- Ctrl+U clears the draft.

Type `/` at the start of the message for commands, or `@` at a token boundary
for workspace paths. The popup shows up to six matches and keeps the input
in place. It filters by prefix; files are browsed one directory at a time,
not by searching the entire repository. Hidden entries appear when you type
a leading dot. Suggestions are available in the idle and running composer,
not in permission dialogs, question prompts, or session search.

- Up/Down selects a match.
- Tab inserts without executing.
- Enter executes a selected command when idle; during a run it queues the
  completed command. For files and directories, Enter inserts without sending.
- Esc dismisses the popup; typing again can reopen it. Press Esc again to
  stop an active run.

An exact completed file path closes its suggestion, so Enter can send the
message. Directory completion adds `/` and keeps browsing. Paths containing
spaces are quoted automatically. `/mode p` and `/mode a` suggest Plan and Act.

## File references

```text
Review @internal/tui/input.go
Compare @"docs/design notes.txt" with @README.md
Describe @screenshot.png
```

Paths are relative to the selected workspace. Text files must be UTF-8 and
contain no NUL bytes. Each text file is limited to 32 KiB; all text references
in a message are limited to 64 KiB combined. Repeated references to the same
resolved file attach one copy. Directories, binary/invalid UTF-8 files,
oversized files, and paths escaping the workspace (including resolved
symlinks) produce an error. No partial attachment is sent on these errors.

The model receives the original text plus file snapshots. The transcript
shows the original message, including after reopening saved history. Queued
references are read when their turn begins; retries reuse that turn's snapshot.
Typing a reference is an explicit user action: it does not invoke the model's
read_file permission gate. Attach only content you intend to send to the
configured backend. See [the trust boundary](../SECURITY.md).

PNG, JPG/JPEG, WebP, GIF, and BMP use image attachments instead of text.
The backend caps each image at 20 MiB. A vision-capable OpenAI-compatible
provider or llama-server with VL weights and `--mmproj` is required. TARS's
KoboldCPP dialect rejects image attachments. `-image` attaches images to
an opening task; inline text-file references are a TUI composer feature,
not syntax interpreted by print mode or RPC.

Bare unknown mentions such as `@someone` remain literal. A missing unquoted
image path also remains literal for compatibility; quoted missing paths and
missing text-file paths with an extension/path separator report an error.

## Function-key footer

- F1 Help: open/close the help modal.
- F2 Search: open/close the centered session search popup.
- F3 Details: expand/collapse thinking and tool output.
- F4 New: start an empty session when idle.
- F5 Mode: switch Plan/Act when idle in a direct chat.
- F6 Sidebar: show/hide the pane when the terminal is wide enough.
- F7 Latest: jump to the latest message.
- F8: reserved, disabled, with no label.
- F9: focus the header menu. Left/Right selects a badge; Enter opens it.
  Up/Down browses details and actions; Enter chooses an action. Esc goes back
  or returns to the composer. You can also click badges and actions.
- F10 Quit: exit TARS.

The footer can be clicked. Disabled actions are dimmed. Below 80 columns,
it uses two rows; very small terminals omit it.

## Sessions

F2 or Ctrl+P opens a centered popup over the chat, using the same palette as
input suggestions. Search matches session names/IDs and saved user messages
and assistant replies, case-insensitively. Title matches rank ahead of history
matches; empty search shows recent sessions. Results show gray ages and a
highlighted matching excerpt. Up/Down and PgUp/PgDn move through results;
Enter opens the session without generating a response. History matches jump
to the matching message. Esc closes and preserves the composer draft.

Search covers the saved `state.json` conversation: compacted-away messages,
system/harness instructions, reasoning, and tool output are not searched.
Stop a running turn before switching sessions.

Focus the Sessions pane with Tab, then use Ctrl+R to rename or Ctrl+D to
confirm deletion. The same shortcuts remain available in the search popup.
Type a follow-up to continue. Saved Plan/Act mode is restored when
opening a direct chat. Missions can be resumed from the CLI; the session
search popup does not resume them as direct chats.

Drafts, input history, and the queued follow-up are kept separately for each
chat while the UI is open. They are not persisted across app restarts. Saved
model history and display metadata live under `.tars/tasks/<id>/`.

## Running, stopping, and permissions

Only one follow-up can be queued. Ctrl+E moves it into an empty draft;
Ctrl+X cancels it. It starts after a successful run if the draft is empty.
Failure, interruption, or a nonempty draft leaves it queued for editing.
Esc stops the run and waits for cleanup; it does not quit or erase the chat.
Ctrl+Q or F10 quits; Ctrl+C aborts and quits.

Permission requests show the tool and resource with a scrollable preview:
Y allows once, A asks for confirmation to allow the same tool/resource for
this run, and N opens an optional redirect note. Enter submits the note;
Esc returns from the note/confirmation step. Esc at the initial permission
prompt denies the request. Explicit policy denial cannot be overridden.
Question prompts accept a multiline answer with Enter.

## Help and local commands

F1 and `/help` open the same modal with Input, Navigate, Run, and Commands
categories. Tab/Shift+Tab or Left/Right switches category; Up/Down,
PgUp/PgDn, and the mouse wheel scroll. Esc, Enter, or F1 closes it.
The draft is preserved while Help is open.

- `/help`: help modal.
- `/sessions`: session search popup.
- `/new [task]`: start a new task; without text, reset to an empty chat.
- `/mode plan|act`: switch direct-chat mode; `/mode` alone shows the mode.
- `/status`: append current run facts to the transcript.
- `/retry`: retry the last failed turn from its saved starting history.
- `/compact`: shrink the saved conversation history when idle.
- `/quit` or `/q`: quit.

Unknown commands show an error and are never sent to the model. These are
local UI commands; they are not tools or RPC methods.

## Troubleshooting

- Server not ready: start `run-kobold.cmd` first and wait for loading to finish.
- Shift+Enter inserts correctly in classic Windows consoles through native
  key records. Other terminals must preserve key modifiers; Ctrl+O remains
  available if a terminal maps Shift+Enter to plain Enter.
- Strange scrolling: keep server logs in their own terminal. Do not redirect
  model/server output into the full-screen UI.
- No streaming text on KoboldCPP: that dialect uses unary requests. OpenAI
  and llama stream automatically in the TUI; the spinner still reports activity
  in unary mode.
- Large reference rejected: use a smaller file or ask the agent to inspect
  it with bounded file tools. Raising the generation budget does not raise
  attachment limits.
