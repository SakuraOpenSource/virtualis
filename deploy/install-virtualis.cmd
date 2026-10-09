@echo off
rem SHA-256 validation and private token-file ACLs are implemented in PowerShell.
rem No fake Windows SCM service success is reported for a console Go binary.
setlocal
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0install-virtualis.ps1" %*
exit /b %errorlevel%
