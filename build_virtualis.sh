#!/usr/bin/env bash
set -e
# 一键构建 Virtualis 主控 + 被控（可在 virtualis 目录内直接执行）
# 用法: bash build_virtualis.sh [--clean] [--no-frontend] [--all]
# 默认仅本机；--all 交叉编译 Win+Mac+Linux 全平台

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# 兼容两种调用位置：virtualis/ 内或 monorepo 根
if [ -f "$SCRIPT_DIR/go.mod" ] && grep -q "module github.com/SakuraOpenSource/virtualis" "$SCRIPT_DIR/go.mod" 2>/dev/null; then
  VIRT="$SCRIPT_DIR"
  ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
else
  ROOT="$SCRIPT_DIR"
  VIRT="$ROOT/virtualis"
fi
FRONT="$ROOT/virtualis-frontend"
AGENT="$ROOT/virtualis-agent"
# 若在 virtualis 内且上层无 monorepo，则回退
if [ ! -d "$FRONT" ] && [ -d "$SCRIPT_DIR/../virtualis-frontend" ]; then FRONT="$SCRIPT_DIR/../virtualis-frontend"; fi
if [ ! -d "$AGENT" ] && [ -d "$SCRIPT_DIR/../virtualis-agent" ]; then AGENT="$SCRIPT_DIR/../virtualis-agent"; fi
BIN_DIR="$VIRT/bin"

GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; NC='\033[0m'

CLEAN=0; NO_FRONTEND=0; ALL=0
for arg in "$@"; do
  case "$arg" in
    --clean) CLEAN=1 ;;
    --no-frontend) NO_FRONTEND=1 ;;
    --all|--release|--cross) ALL=1 ;;
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
echo -e "${GREEN}主控本机构建完成: $BIN_DIR/virtualis${NC}"
if [ "$ALL" -eq 1 ]; then
  echo -e "${GREEN}=== 交叉编译主控全平台 (Win/Mac/Linux) ===${NC}"
  for p in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do
    GOOS="${p%/*}"; GOARCH="${p#*/}"; EXT=""; [ "$GOOS" = "windows" ] && EXT=".exe"
    OUT="$BIN_DIR/virtualis-${GOOS}-${GOARCH}${EXT}"
    echo -e "${YELLOW}  -> $GOOS/$GOARCH${NC}"
    (cd "$VIRT" && CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" go build -trimpath -ldflags "-s -w" -o "$OUT" ./cmd/virtualis)
    ls -lh "$OUT"
  done
fi

echo -e "${GREEN}=== 构建 Virtualis 被控 (Go, 无前端) ===${NC}"
if [ -d "$AGENT" ]; then
  mkdir -p "$AGENT/bin"
  (cd "$AGENT" && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$AGENT/bin/virtualis-agent" ./cmd/agent)
  ls -lh "$AGENT/bin/virtualis-agent"
  cp "$AGENT/bin/virtualis-agent" "$BIN_DIR/virtualis-agent" 2>/dev/null || true
  echo -e "${GREEN}被控本机构建完成: $AGENT/bin/virtualis-agent${NC}"
  if [ "$ALL" -eq 1 ]; then
    echo -e "${GREEN}=== 交叉编译被控全平台 ===${NC}"
    for p in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do
      GOOS="${p%/*}"; GOARCH="${p#*/}"; EXT=""; [ "$GOOS" = "windows" ] && EXT=".exe"
      OUT="$AGENT/bin/virtualis-agent-${GOOS}-${GOARCH}${EXT}"
      OUT2="$BIN_DIR/virtualis-agent-${GOOS}-${GOARCH}${EXT}"
      echo -e "${YELLOW}  -> $GOOS/$GOARCH${NC}"
      (cd "$AGENT" && CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" go build -trimpath -ldflags "-s -w" -o "$OUT" ./cmd/agent)
      ls -lh "$OUT"
      cp "$OUT" "$OUT2" 2>/dev/null || true
    done
  fi
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
ls -lh "$BIN_DIR/" 2>/dev/null | sed 's/^/  /'
if [ -d "$AGENT/bin" ]; then ls -lh "$AGENT/bin/" 2>/dev/null | sed 's/^/  /'; fi
echo ""
echo "运行主控: $BIN_DIR/virtualis -data ./data"
echo "运行被控: $BIN_DIR/virtualis-agent --master http://MASTER:8080 --token <token> --name node-01"
if [ "$ALL" -eq 1 ]; then echo ""; echo "全平台产物位于 $BIN_DIR/ 与 $AGENT/bin/ 下，按 GOOS-GOARCH 命名，.exe 为 Windows"; fi
