@echo off
REM Local TUI launcher (machine-specific: ports, backends). Gitignored.
REM Opens the fullscreen chat UI with no initial task - type it in.
REM Needs a real terminal (TTY).

go run ./cmd/agent -tui -backend-kind kobold -backend http://127.0.0.1:5001 -workspace . -yes -no-verify %*