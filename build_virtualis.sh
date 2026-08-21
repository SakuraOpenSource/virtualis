#!/usr/bin/env bash
set -e
# 一键构建 Virtualis 主控 + 被控
# 用法: bash build_virtualis.sh [--clean] [--no-frontend]
# 产物: virtualis/bin/virtualis, virtualis-agent/bin/virtualis-agent, 前端注入 internal/web/dist

ROOT="$(cd "$(dirname "$0")" && pwd)"
VIRT="$ROOT/virtualis"
FRONT="$ROOT/virtualis-frontend"
AGENT="$ROOT/virtualis-agent"
BIN_DIR="$VIRT/bin"

GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; NC='\033[0m'

CLEAN=0; NO_FRONTEND=0
for arg in "$@"; do
  case "$arg" in
    --clean) CLEAN=1 ;;
    --no-frontend) NO_FRONTEND=1 ;;
  esac
done

if [ "$CLEAN" -eq 1 ]; then
  echo -e "${YELLOW}清理旧产物...${NC}"
  rm -rf "$VIRT/bin" "$VIRT/internal/web/dist" "$AGENT/bin"
  mkdir -p "$VIRT/internal/web/dist" && touch "$VIRT/internal/web/dist/.gitkeep"
fi

echo -e "${GREEN}=== 构建 Virtualis 前端 ===${NC}"
if [ "$NO_FRONTEND" -eq 0 ]; then
  if [ ! -d "$FRONT" ]; then echo -e "${RED}前端目录不存在: $FRONT${NC}"; exit 1; fi
  if ! command -v pnpm >/dev/null 2>&1; then echo -e "${RED}未找到 pnpm，请先安装 Node/pnpm${NC}"; exit 1; fi
  (cd "$FRONT" && pnpm install --frozen-lockfile && pnpm build)
  rm -rf "$VIRT/internal/web/dist"
  mkdir -p "$VIRT/internal/web/dist"
  cp -R "$FRONT/dist/." "$VIRT/internal/web/dist/"
  touch "$VIRT/internal/web/dist/.gitkeep"
  echo -e "${GREEN}前端已注入 $VIRT/internal/web/dist${NC}"
else
  echo -e "${YELLOW}跳过前端构建${NC}"
fi

echo -e "${GREEN}=== 构建 Virtualis 主控 (Go) ===${NC}"
if ! command -v go >/dev/null 2>&1; then echo -e "${RED}未找到 go${NC}"; exit 1; fi
mkdir -p "$BIN_DIR"
(cd "$VIRT" && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$BIN_DIR/virtualis" ./cmd/virtualis)
ls -lh "$BIN_DIR/virtualis"
echo -e "${GREEN}主控构建完成: $BIN_DIR/virtualis${NC}"

echo -e "${GREEN}=== 构建 Virtualis 被控 (Go, 无前端) ===${NC}"
if [ -d "$AGENT" ]; then
  mkdir -p "$AGENT/bin"
  (cd "$AGENT" && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$AGENT/bin/virtualis-agent" ./cmd/agent)
  ls -lh "$AGENT/bin/virtualis-agent"
  cp "$AGENT/bin/virtualis-agent" "$BIN_DIR/virtualis-agent" 2>/dev/null || true
  echo -e "${GREEN}被控构建完成: $AGENT/bin/virtualis-agent${NC}"
else
  if [ -d "$VIRT/agent" ]; then
    (cd "$VIRT" && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$BIN_DIR/virtualis-agent" ./agent/cmd/agent 2>/dev/null || CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$BIN_DIR/virtualis-agent" ./cmd/agent 2>/dev/null || true)
    ls -lh "$BIN_DIR/virtualis-agent" 2>/dev/null || echo -e "${YELLOW}被控源码未找到，跳过${NC}"
  else
    echo -e "${YELLOW}未找到 virtualis-agent 仓库，跳过被控构建${NC}"
  fi
fi

echo ""
echo -e "${GREEN}=== 构建完成 ===${NC}"
echo "主控: $BIN_DIR/virtualis ($(du -h "$BIN_DIR/virtualis" | cut -f1))"
if [ -f "$BIN_DIR/virtualis-agent" ]; then echo "被控: $BIN_DIR/virtualis-agent ($(du -h "$BIN_DIR/virtualis-agent" | cut -f1))"; fi
if [ -f "$AGENT/bin/virtualis-agent" ]; then echo "被控: $AGENT/bin/virtualis-agent"; fi
echo ""
echo "运行主控: $BIN_DIR/virtualis -data ./data"
echo "运行被控: $BIN_DIR/virtualis-agent --master http://MASTER:8080 --token <token> --name node-01"
