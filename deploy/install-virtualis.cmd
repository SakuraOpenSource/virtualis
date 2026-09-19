@echo off
REM ==============================================================================
REM Virtualis Windows 一键安装（主控 / 被控）
REM
REM 目录约定：
REM   C:\opt\virtualis\master    主控
REM   C:\opt\virtualis\agent     被控
REM
REM 被控安装示例（管理员 CMD）：
REM   install-virtualis.cmd --agent --master-url http://MASTER:8080 --token TOKEN --name node-01
REM agent 从 GitHub virtualis-agent release 下载最新版；失败时回退主控分发端点。
REM ==============================================================================
setlocal enabledelayedexpansion

set ROLE=master
set MASTER_URL=
set TOKEN=
set AGENT_NAME=
set MODE=agent-only

:parse
if "%~1"=="" goto done_parse
if /I "%~1"=="--agent" set ROLE=agent
if /I "%~1"=="--master" set ROLE=master
if /I "%~1"=="--master-url" ( set "MASTER_URL=%~2" & shift )
if /I "%~1"=="--token" ( set "TOKEN=%~2" & shift )
if /I "%~1"=="--name" ( set "AGENT_NAME=%~2" & shift )
if /I "%~1"=="--backends" ( set "MODE=%~2" & shift )
shift
goto parse
:done_parse

echo === Virtualis Windows 安装 (%ROLE%) ===

if "%ROLE%"=="agent" goto agent

REM ---------------- 主控 ----------------
if not exist "C:\opt\virtualis\master" mkdir "C:\opt\virtualis\master"
if not exist "C:\opt\virtualis\master\data" mkdir "C:\opt\virtualis\master\data"

where go >nul 2>&1
if %errorlevel%==0 if exist go.mod (
  echo 检测到源码，本地构建主控...
  set CGO_ENABLED=0
  go build -trimpath -ldflags "-s -w" -o "C:\opt\virtualis\master\virtualis.exe" .\cmd\virtualis
  if errorlevel 1 ( echo 构建失败 & pause & exit /b 1 )
) else (
  echo 从 GitHub Releases 下载主控...
  powershell -Command "Invoke-WebRequest -Uri 'https://github.com/SakuraOpenSource/virtualis/releases/latest/download/virtualis-windows-amd64.exe' -OutFile 'C:\opt\virtualis\master\virtualis.exe'"
  if errorlevel 1 ( echo 下载失败，请手动下载: https://github.com/SakuraOpenSource/virtualis/releases & pause & exit /b 1 )
)

echo 创建 Windows 服务...
sc create Virtualis binPath= "\"C:\opt\virtualis\master\virtualis.exe\" -data \"C:\opt\virtualis\master\data\"" start= auto >nul 2>&1
sc start Virtualis >nul 2>&1
echo 主控目录: C:\opt\virtualis\master
echo 安装向导: http://localhost:8080
goto done

REM ---------------- 被控 ----------------
:agent
if "%MASTER_URL%"=="" (
  set /p MASTER_URL="主控地址 (例如 http://10.0.0.1:8080): "
)
if "%TOKEN%"=="" (
  set /p TOKEN="被控 Token: "
)
if "%AGENT_NAME%"=="" (
  set "AGENT_NAME=node-%COMPUTERNAME%"
)

if not exist "C:\opt\virtualis\agent" mkdir "C:\opt\virtualis\agent"
if not exist "C:\opt\virtualis\agent\data" mkdir "C:\opt\virtualis\agent\data"

echo 从 GitHub virtualis-agent release 下载最新被控...
powershell -Command "[Net.ServicePointManager]::SecurityProtocol=[Net.SecurityProtocolType]::Tls12; Invoke-WebRequest -Uri 'https://github.com/SakuraOpenSource/virtualis-agent/releases/latest/download/virtualis-agent-windows-amd64.exe' -OutFile 'C:\opt\virtualis\agent\virtualis-agent.exe'"
if errorlevel 1 (
  echo GitHub 下载失败，尝试主控分发端点...
  powershell -Command "[Net.ServicePointManager]::SecurityProtocol=[Net.SecurityProtocolType]::Tls12; Invoke-WebRequest -Uri '%MASTER_URL%/api/agent/binary?os=windows^&arch=amd64' -OutFile 'C:\opt\virtualis\agent\virtualis-agent.exe'"
  if errorlevel 1 ( echo 两条下载路径均失败 & pause & exit /b 1 )
)

echo 创建 Windows 服务...
sc create VirtualisAgent binPath= "\"C:\opt\virtualis\agent\virtualis-agent.exe\" --master %MASTER_URL% --token %TOKEN% --name %AGENT_NAME% --data \"C:\opt\virtualis\agent\data\"" start= auto >nul 2>&1
sc start VirtualisAgent >nul 2>&1
echo 被控目录: C:\opt\virtualis\agent
echo 名称: %AGENT_NAME%   主控: %MASTER_URL%

:done
echo.
echo 完成。
pause
