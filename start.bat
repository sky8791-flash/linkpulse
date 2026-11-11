@echo off
setlocal
cd /d "%~dp0"

where pwsh.exe >nul 2>nul
if errorlevel 1 (
    echo PowerShell 7 ^(pwsh^) was not found.
    echo Install PowerShell 7 or run scripts\start.ps1 with PowerShell 7.
    pause
    exit /b 1
)

pwsh.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0scripts\start.ps1"
set EXIT_CODE=%ERRORLEVEL%

if not "%EXIT_CODE%"=="0" (
    echo.
    echo Startup failed. Check the message above and logs\server-*.err.log.
)

echo.
pause
exit /b %EXIT_CODE%
