@echo off
setlocal
REM Run this in its own terminal. Keep it open while using TARS.
set "KOBOLD_EXE=E:\Projects\ai-text\koboldcpp-extracted\koboldcpp-launcher.exe"
set "MODEL_FILE=E:\Projects\ai-text\models\unsloth\Qwen3.5-9B-GGUF\Qwen3.5-9B-Q4_K_M.gguf"

if not exist "%KOBOLD_EXE%" (
  echo KoboldCPP not found: %KOBOLD_EXE%
  goto failed
)
if not exist "%MODEL_FILE%" (
  echo Model not found: %MODEL_FILE%
  goto failed
)
curl.exe --silent --fail --max-time 2 http://127.0.0.1:5001/api/extra/version >nul 2>&1
if not errorlevel 1 (
  echo KoboldCPP is already running on port 5001. Run run-tui.cmd in another terminal.
  pause
  exit /b 0
)
cd /d "E:\Projects\ai-text\koboldcpp-extracted"
"%KOBOLD_EXE%" --model "%MODEL_FILE%" --usecuda 0 --gpulayers 999 --contextsize 16384 --quantkv 1 --host 127.0.0.1 --port 5001 --skiplauncher
if errorlevel 1 goto failed
exit /b 0

:failed
echo KoboldCPP startup failed. See the output above.
pause
exit /b 1
