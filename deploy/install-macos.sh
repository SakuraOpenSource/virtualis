#!/usr/bin/env bash
set -e
# Virtualis macOS 部署脚本
# 支持选择 QEMU / Incus / LXC，并安装 Virtualis

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'

ROLE="master"
for arg in "$@"; do
  case "$arg" in
    --agent) ROLE="agent" ;;
  esac
done

echo -e "${GREEN}=== Virtualis macOS 部署 ===${NC}"

if ! command -v brew >/dev/null 2>&1; then
  echo -e "${RED}未找到 Homebrew，请先安装: https://brew.sh${NC}"
  echo '/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"'
  exit 1
fi

echo "选择部署角色:"
echo "  1) 主控 (含前端)"
echo "  2) 被控 (仅后端)"
read -p "输入 1/2 [1]: " rc
if [ "$rc" = "2" ]; then ROLE="agent"; fi
echo -e "${YELLOW}已选择: $ROLE${NC}"

echo ""
echo "选择虚拟化后端 (可多选，空格分隔):"
echo "  1) QEMU   - qemu (brew install qemu)"
echo "  2) LXC    - lxc (brew install lxc) - macOS 支持有限"
echo "  3) Incus  - incus (brew install incus) - 需 Linux 容器支持"
read -p "你的选择 [1]: " sel
sel=${sel:-1}

need_qemu=0; need_lxc=0; need_incus=0
for s in $sel; do
  case "$s" in 1) need_qemu=1;; 2) need_lxc=1;; 3) need_incus=1;; esac
done

if [ "$need_qemu" -eq 1 ]; then
  echo -e "${GREEN}安装 QEMU...${NC}"
  brew install qemu || true
fi
if [ "$need_lxc" -eq 1 ]; then
  echo -e "${GREEN}安装 LXC...${NC}"
  brew install lxc || echo -e "${YELLOW}LXC 在 macOS 上功能受限，建议使用 QEMU/Incus (Linux)${NC}"
fi
if [ "$need_incus" -eq 1 ]; then
  echo -e "${GREEN}安装 Incus...${NC}"
  brew install incus || echo -e "${YELLOW}Incus 在 macOS 上需 Linux 宿主机，macOS 建议 QEMU${NC}"
fi

echo ""
echo -e "${GREEN}安装 Virtualis ($ROLE)...${NC}"

if [ -f "go.mod" ] && [ -d "internal" ]; then
  echo "本地构建..."
  if ! command -v go >/dev/null 2>&1; then echo -e "${RED}未找到 go${NC}"; exit 1; fi
  if [ "$ROLE" = "master" ]; then
    if ! command -v pnpm >/dev/null 2>&1; then npm install -g pnpm || true; fi
    if [ -d "../virtualis-frontend" ]; then
      (cd ../virtualis-frontend && pnpm install --frozen-lockfile && pnpm build)
      rm -rf internal/web/dist
      mkdir -p internal/web/dist
      cp -R ../virtualis-frontend/dist/. internal/web/dist/
      touch internal/web/dist/.gitkeep
    fi
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /tmp/virtualis ./cmd/virtualis
    sudo install -m 755 /tmp/virtualis /usr/local/bin/virtualis
    echo -e "${GREEN}已安装到 /usr/local/bin/virtualis${NC}"
    echo "运行: virtualis -data ./data"
    echo "或创建 launchd 服务: sudo cp deploy/com.virtualis.plist /Library/LaunchDaemons/ && sudo launchctl load /Library/LaunchDaemons/com.virtualis.plist"
  else
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /tmp/virtualis-agent ./cmd/agent 2>/dev/null || CGO_ENABLED=0 go build -o /tmp/virtualis-agent ./agent/cmd/agent 2>/dev/null || {
      CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /tmp/virtualis-agent ./cmd/virtualis
    }
    sudo install -m 755 /tmp/virtualis-agent /usr/local/bin/virtualis-agent
    echo -e "${GREEN}被控已安装到 /usr/local/bin/virtualis-agent${NC}"
    echo "从主控生成指令后执行: sudo virtualis-agent --master http://MASTER:8080 --token TOKEN --name node-01"
  fi
else
  echo "未找到源码，请从 GitHub 下载二进制或 clone 仓库后重新运行"
fi

echo -e "${GREEN}完成${NC}"
