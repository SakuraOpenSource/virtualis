@echo off
REM Virtualis Windows 部署批处理
REM 支持选择 QEMU / Mock，并安装 Virtualis 主控/被控
REM 需以管理员身份运行

setlocal enabledelayedexpansion

echo === Virtualis Windows 部署 ===

set ROLE=master
for %%a in (%*) do (
  if "%%a"=="--agent" set ROLE=agent
)

echo 选择部署角色:
echo   1) 主控 (含前端)
echo   2) 被控 (仅后端)
set /p role_choice="输入 1/2 [1]: "
if "%role_choice%"=="2" set ROLE=agent
if "%ROLE%"=="agent" (
  echo 已选择: 被控
) else (
  echo 已选择: 主控
)

echo.
echo 选择虚拟化后端 (可多选，空格分隔):
echo   1) Mock   - 模拟 (无需安装)
echo   2) QEMU   - qemu (通过 winget/choco 安装)
echo   3) LXC    - 在 Windows 上不可用，建议 WSL2
echo   4) Incus  - 在 Windows 上不可用，建议 WSL2/Linux
echo   例: 输入 2 安装 QEMU，回车默认 1
set /p sel="你的选择 [1]: "
if "%sel%"=="" set sel=1

echo %sel% | findstr "2" >nul
if %errorlevel%==0 (
  echo 安装 QEMU...
  where winget >nul 2>&1
  if %errorlevel%==0 (
    winget install --id SoftwareFreedomConservancy.QEMU -e --accept-package-agreements
  ) else (
    where choco >nul 2>&1
    if %errorlevel%==0 (
      choco install qemu -y
    ) else (
      echo 未找到 winget/choco，请手动安装 QEMU: https://www.qemu.org/download/
    )
  )
) else (
  echo 跳过 QEMU 安装 (Mock 模式)
)

echo %sel% | findstr "3" >nul
if %errorlevel%==0 (
  echo 提示: LXC 在 Windows 原生不支持，建议使用 WSL2 + Linux 发行版
)

echo %sel% | findstr "4" >nul
if %errorlevel%==0 (
  echo 提示: Incus 在 Windows 原生不支持，建议使用 WSL2 + Linux
)

echo.
echo 安装 Virtualis (%ROLE%)...

REM 检查是否在源码目录
if exist go.mod (
  echo 检测到源码，本地构建...
  where go >nul 2>&1
  if %errorlevel% neq 0 (
    echo 未找到 go，请先安装 Go 1.22+: https://go.dev/dl/
    pause
    exit /b 1
  )
  if "%ROLE%"=="master" (
    where pnpm >nul 2>&1
    if %errorlevel% neq 0 (
      echo 安装 pnpm...
      npm install -g pnpm
    )
    if exist ..\virtualis-frontend\package.json (
      echo 构建前端...
      pushd ..\virtualis-frontend
      call pnpm install --frozen-lockfile
      call pnpm build
      popd
      rmdir /s /q internal\web\dist 2>nul
      mkdir internal\web\dist
      xcopy /E /I ..\virtualis-frontend\dist\* internal\web\dist\
      type nul > internal\web\dist\.gitkeep
    )
    set CGO_ENABLED=0
    go build -trimpath -ldflags "-s -w" -o "%TEMP%\virtualis.exe" ./cmd/virtualis
    if %errorlevel% neq 0 (
      echo 构建失败
      pause
      exit /b 1
    )
    copy /Y "%TEMP%\virtualis.exe" "C:\Program Files\Virtualis\virtualis.exe"
    echo 已安装到 C:\Program Files\Virtualis\virtualis.exe
    echo.
    echo 创建 Windows 服务 (需 NSSM 或 sc)...
    echo   sc create Virtualis binPath= "\"C:\Program Files\Virtualis\virtualis.exe\" -data \"C:\ProgramData\Virtualis\""
    echo   sc start Virtualis
    echo 或直接运行: "C:\Program Files\Virtualis\virtualis.exe" -data .\data
  ) else (
    echo 构建被控...
    set CGO_ENABLED=0
    go build -trimpath -ldflags "-s -w" -o "%TEMP%\virtualis-agent.exe" ./cmd/agent 2>nul
    if %errorlevel% neq 0 (
      go build -trimpath -ldflags "-s -w" -o "%TEMP%\virtualis-agent.exe" ./cmd/virtualis
    )
    copy /Y "%TEMP%\virtualis-agent.exe" "C:\Program Files\Virtualis\virtualis-agent.exe"
    echo 已安装到 C:\Program Files\Virtualis\virtualis-agent.exe
    echo.
    echo 请从主控生成接入指令后在被控执行，例如:
    echo   "C:\Program Files\Virtualis\virtualis-agent.exe" --master http://MASTER_IP:8080 --token JOIN_TOKEN --name node-01
  )
) else (
  echo 未找到源码，请从 GitHub Releases 下载 virtualis-windows-amd64.exe
  echo   https://github.com/SakuraOpenSource/virtualis/releases
)

echo.
echo 部署完成！
if "%ROLE%"=="master" (
  echo 访问 http://localhost:8080 完成安装
) else (
  echo 被控安装完成，等待接入主控
)
pause
