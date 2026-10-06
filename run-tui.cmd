@echo off
setlocal
REM Connect to the server started separately with run-kobold.cmd.
cd /d "%~dp0"
curl.exe --silent --fail --max-time 3 http://127.0.0.1:5001/api/v1/model >nul 2>&1
if errorlevel 1 (
  echo KoboldCPP is not ready. Start run-kobold.cmd and wait for it to finish loading.
  pause
  exit /b 1
)
go build -o agent.exe ./cmd/agent
if errorlevel 1 (
  pause
  exit /b 1
)
"%~dp0agent.exe" -tui -backend-kind kobold -backend http://127.0.0.1:5001 -model Qwen3.5-9B-Q4_K_M -workspace "%~dp0." -max-tokens 4096 %*
exit /b %errorlevel%
