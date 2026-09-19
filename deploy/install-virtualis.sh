#!/usr/bin/env bash
# ==============================================================================
# Virtualis 一键安装脚本（Linux / macOS）—— 主控与被控二合一
#
# 用法：
#   主控:  sudo bash install-virtualis.sh                 # 交互式（默认主控）
#   被控:  sudo bash install-virtualis.sh --agent \
#            --master http://MASTER:8080 --token TOKEN [--name node-01]
#   升级:  加 --update（保留现有 systemd 配置与数据）
#
# 可选环境变量 / 参数：
#   --version VERSION     agent/主控版本（默认 latest，GitHub Releases 拉取）
#   --gh-proxy URL        GitHub 代理（如 https://gh-proxy.org）
#   --backends LIST       被控虚拟化后端：qemu,lxc,incus（缺省交互选择）
#
# 目录约定（升级互不影响）：
#   /opt/virtualis/master    主控二进制 + data/
#   /opt/virtualis/agent     被控二进制 + data/
# ==============================================================================
set -Eeuo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
AGENT_REPO="SakuraOpenSource/virtualis-agent"
MASTER_REPO="SakuraOpenSource/virtualis"
MASTER_DIR="/opt/virtualis/master"
AGENT_DIR="/opt/virtualis/agent"
VERSION="latest"
ROLE=""
UPDATE=0
NO_START=0
GH_PROXY=""
MASTER_URL=""; TOKEN=""; AGENT_NAME=""
BACKENDS=""

log()  { echo -e "${GREEN}[virtualis]${NC} $*"; }
warn() { echo -e "${YELLOW}[virtualis]${NC} $*"; }
die()  { echo -e "${RED}[virtualis]${NC} $*" >&2; exit 1; }

usage() { sed -n '2,24p' "$0" | sed 's/^# \{0,1\}//'; exit 0; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --agent) ROLE="agent"; shift;;
    --master) ROLE="master"; shift;;
    --master-url) MASTER_URL="${2:-}"; shift 2;;
    --token) TOKEN="${2:-}"; shift 2;;
    --name) AGENT_NAME="${2:-}"; shift 2;;
    --version) VERSION="${2:-latest}"; shift 2;;
    --backends|--backend) BACKENDS="${2:-}"; shift 2;;
    --gh-proxy) GH_PROXY="${2:-}"; shift 2;;
    --update) UPDATE=1; shift;;
    --no-start) NO_START=1; shift;;
    -h|--help) usage;;
    *) die "未知参数: $1（--help 查看用法）";;
  esac
done

run_root() { if [[ "$(id -u)" -eq 0 ]]; then "$@"; else sudo "$@"; fi; }

detect_platform() {
  local os arch
  os="$(uname -s)"; arch="$(uname -m)"
  case "$os" in
    Linux)  PLATFORM="linux";;
    Darwin) PLATFORM="darwin";;
    *) die "不支持的操作系统: $os（Windows 请使用 install-virtualis.cmd）";;
  esac
  case "$arch" in
    x86_64|amd64)  GOARCH="amd64";;
    arm64|aarch64) GOARCH="arm64";;
    *) die "不支持的 CPU 架构: $arch";;
  esac
}

download() {
  local url="$1" out="$2"
  if [[ -n "$GH_PROXY" && "$url" == *"github.com"* ]]; then
    url="${GH_PROXY%/}/$url"
  fi
  log "下载 $url"
  if command -v curl >/dev/null 2>&1; then
    run_root curl -fSL --retry 3 -o "$out" "$url" || die "下载失败: $url"
  elif command -v wget >/dev/null 2>&1; then
    run_root wget -qO "$out" "$url" || die "下载失败: $url"
  else
    die "需要 curl 或 wget"
  fi
  [[ -s "$out" ]] || die "下载得到空文件: $url"
}

resolve_version() {
  [[ "$VERSION" != "latest" ]] && return
  log "获取 virtualis-agent 最新 release 版本..."
  VERSION="$(run_root curl -fsSLI -o /dev/null -w '%{url_effective}' \
    "https://github.com/$AGENT_REPO/releases/latest" | sed 's#.*/tag/##')" || true
  [[ -n "$VERSION" ]] || VERSION="latest"
  log "版本: $VERSION"
}

verify_binary_magic() {
  local file="$1" magic
  magic="$(od -An -tx1 -N4 "$file" 2>/dev/null | tr -d ' \n')"
  case "$PLATFORM" in
    linux)  [[ "$magic" == "7f454c46" ]] || die "下载内容不是有效的 Linux 二进制";;
    darwin) [[ "$magic" == "cffaedfe" || "$magic" == "feedfacf" || "$magic" == "cafebabe" ]] || die "下载内容不是有效的 macOS 二进制";;
  esac
}

# ---------- 主控 ----------
install_master() {
  run_root mkdir -p "$MASTER_DIR/data"
  local bin="/tmp/virtualis-master.$$"

  # 仓库内执行 → 源码构建（可嵌入前端）；否则拉 release。
  if [[ -f "go.mod" && -d "cmd/virtualis" ]]; then
    command -v go >/dev/null 2>&1 || die "源码构建需要 Go 1.25+"
    log "从源码构建主控..."
    if [[ -d "../virtualis-frontend" ]]; then
      log "构建前端..."
      if (cd ../virtualis-frontend && (command -v pnpm >/dev/null 2>&1 || npm install -g pnpm) \
          && pnpm install --frozen-lockfile && pnpm build); then
        run_root rm -rf internal/web/dist && run_root mkdir -p internal/web/dist
        run_root cp -R ../virtualis-frontend/dist/. internal/web/dist/
        run_root touch internal/web/dist/.gitkeep
      else
        warn "前端构建失败，仅后端可用"
      fi
    fi
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$bin" ./cmd/virtualis
  else
    local asset="virtualis-$PLATFORM-$GOARCH"
    download "https://github.com/$MASTER_REPO/releases/download/$VERSION/$asset" "$bin"
    verify_binary_magic "$bin"
  fi
  run_root install -m 755 "$bin" "$MASTER_DIR/virtualis"
  run_root rm -f "$bin"

  # 主控分发的被控包优先本地 agent-packages；否则从 GitHub agent release 同步。
  local pkgdir="$MASTER_DIR/agent-packages"
  run_root mkdir -p "$pkgdir"
  if [[ -d "agent-packages" ]] && compgen -G "agent-packages/virtualis-agent-*" >/dev/null; then
    run_root install -m 0755 agent-packages/virtualis-agent-* "$pkgdir/" 2>/dev/null || true
  else
    resolve_version
    local a arch
    for arch in amd64 arm64; do
      a="/tmp/virtualis-agent-linux-$arch.$$"
      if download "https://github.com/$AGENT_REPO/releases/download/$VERSION/virtualis-agent-linux-$arch" "$a" 2>/dev/null; then
        run_root install -m 0755 "$a" "$pkgdir/virtualis-agent-linux-$arch"
      else
        warn "跳过 virtualis-agent-linux-$arch（下载失败）"
      fi
      run_root rm -f "$a"
    done
  fi

  if command -v systemctl >/dev/null 2>&1 && [[ "$(uname -s)" == "Linux" ]]; then
    run_root tee /etc/systemd/system/virtualis.service >/dev/null <<EOF
[Unit]
Description=Virtualis Master
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
WorkingDirectory=$MASTER_DIR
ExecStart=$MASTER_DIR/virtualis -data $MASTER_DIR/data
Restart=always
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF
    run_root systemctl daemon-reload
    run_root systemctl enable virtualis >/dev/null
    if [[ "$NO_START" -eq 0 ]]; then
      run_root systemctl restart virtualis
      log "主控已启动: systemctl status virtualis"
    fi
  else
    warn "请手动运行: $MASTER_DIR/virtualis -data $MASTER_DIR/data"
  fi
  log "主控目录: $MASTER_DIR   安装向导: http://<本机IP>:8080"
}

# ---------- 被控 ----------
install_agent() {
  if [[ "$UPDATE" -eq 0 ]]; then
    [[ -n "$MASTER_URL" && -n "$TOKEN" ]] || die "被控安装需要 --master-url 与 --token"
  fi
  run_root mkdir -p "$AGENT_DIR/data"

  # agent 一律从 GitHub virtualis-agent release 获取最新版；
  # GitHub 不可达时回退到主控分发端点 /api/agent/binary。
  resolve_version
  local suffix=""; [[ "$PLATFORM" == "windows" ]] && suffix=".exe"
  local asset="virtualis-agent-$PLATFORM-$GOARCH$suffix"
  local tmp; tmp="$(mktemp "${TMPDIR:-/tmp}/virtualis-agent.XXXXXX")"
  if ! run_root curl -fSL --retry 3 -o "$tmp" \
      "https://github.com/$AGENT_REPO/releases/download/$VERSION/$asset" 2>/dev/null; then
    warn "GitHub 下载失败，回退主控分发端点..."
    [[ -n "$MASTER_URL" ]] || die "未提供 --master-url，无法回退"
    run_root curl -fSL -o "$tmp" "$MASTER_URL/api/agent/binary?os=$PLATFORM&arch=$GOARCH" \
      || die "两条下载路径均失败，请手动下载 $asset"
  fi
  verify_binary_magic "$tmp"

  run_root install -m 755 "$tmp" "$AGENT_DIR/virtualis-agent"
  run_root rm -f "$tmp"

  AGENT_NAME="${AGENT_NAME:-node-$(hostname -s 2>/dev/null || echo 01)}"

  # systemd 单元：升级且已有单元时保留原 ExecStart（token/名称不变）。
  local unit=/etc/systemd/system/virtualis-agent.service
  if command -v systemctl >/dev/null 2>&1 && [[ "$(uname -s)" == "Linux" ]]; then
    if [[ "$UPDATE" -eq 1 && -f "$unit" ]]; then
      run_root systemctl daemon-reload
      run_root systemctl enable virtualis-agent >/dev/null
      if [[ "$NO_START" -eq 0 ]]; then run_root systemctl restart virtualis-agent; fi
    else
      run_root tee "$unit" >/dev/null <<EOF
[Unit]
Description=Virtualis Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
WorkingDirectory=$AGENT_DIR
ExecStart=$AGENT_DIR/virtualis-agent --master $MASTER_URL --token $TOKEN --name $AGENT_NAME --data $AGENT_DIR/data
Restart=always
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF
      run_root systemctl daemon-reload
      run_root systemctl enable virtualis-agent >/dev/null
      if [[ "$NO_START" -eq 0 ]]; then
        run_root systemctl restart virtualis-agent
        log "被控已启动: systemctl status virtualis-agent"
      fi
    fi
  else
    warn "请手动运行:"
    echo "  $AGENT_DIR/virtualis-agent --master $MASTER_URL --token $TOKEN --name $AGENT_NAME --data $AGENT_DIR/data"
  fi
  log "被控目录: $AGENT_DIR   名称: $AGENT_NAME   主控: $MASTER_URL"
}

# ---------- 虚拟化后端（仅被控，Linux） ----------
install_backends() {
  [[ "$(uname -s)" == "Linux" ]] || { warn "macOS 虚拟化后端请手动安装（brew install qemu）"; return; }
  local pm=""
  if command -v apt-get >/dev/null 2>&1; then pm="apt"
  elif command -v dnf >/dev/null 2>&1; then pm="dnf"
  elif command -v yum >/dev/null 2>&1; then pm="yum"
  elif command -v pacman >/dev/null 2>&1; then pm="pacman"
  elif command -v apk >/dev/null 2>&1; then pm="apk"
  fi
  [[ -n "$pm" ]] || { warn "未知包管理器，请手动安装虚拟化后端"; return; }
  pkg_install() {
    case "$pm" in
      apt) DEBIAN_FRONTEND=noninteractive run_root apt-get update; DEBIAN_FRONTEND=noninteractive run_root apt-get install -y "$@";;
      dnf) run_root dnf install -y "$@";;
      yum) run_root yum install -y "$@";;
      pacman) run_root pacman -Sy --noconfirm "$@";;
      apk) run_root apk add "$@";;
    esac
  }
  local b
  for b in $BACKENDS; do
    case "$b" in
      qemu|qemu-kvm)
        log "安装 QEMU/libvirt..."
        case "$pm" in
          apt) pkg_install qemu-kvm qemu-utils libvirt-daemon-system libvirt-clients;;
          dnf|yum) pkg_install qemu-kvm qemu-img libvirt;;
          pacman) pkg_install qemu-desktop libvirt;;
          apk) pkg_install qemu-img libvirt;;
        esac
        run_root systemctl enable --now libvirtd 2>/dev/null || true
        ;;
      lxc)
        log "安装 LXC..."
        pkg_install lxc lxc-templates uidmap 2>/dev/null || pkg_install lxc
        ;;
      incus)
        log "安装 Incus..."
        pkg_install incus 2>/dev/null || warn "软件源无 Incus，请按 https://linuxcontainers.org/incus/docs/main/install_incus/ 配置仓库"
        if command -v incus >/dev/null 2>&1 && ! run_root incus info >/dev/null 2>&1; then
          run_root incus admin init --auto || true
        fi
        ;;
      *) warn "未知后端: $b";;
    esac
  done
}

# ---------- main ----------
detect_platform

if [[ -z "$ROLE" ]]; then
  echo "请选择安装角色："
  echo "  1) 主控（/opt/virtualis/master）"
  echo "  2) 被控（/opt/virtualis/agent）"
  read -r -p "选择 [1]: " choice < /dev/tty || choice=1
  [[ "$choice" == "2" ]] && ROLE="agent" || ROLE="master"
fi

if [[ "$ROLE" == "agent" ]]; then
  if [[ -z "$BACKENDS" && "$UPDATE" -eq 0 && -t 0 ]]; then
    echo "选择虚拟化后端（可多选，空格分隔，回车跳过）:"
    echo "  1) QEMU   2) LXC   3) Incus"
    read -r -p "选择: " sel < /dev/tty || sel=""
    for s in $sel; do
      case "$s" in
        1) BACKENDS+=" qemu";;
        2) BACKENDS+=" lxc";;
        3) BACKENDS+=" incus";;
      esac
    done
  fi
  BACKENDS="$(printf '%s' "$BACKENDS" | tr ',' ' ' | xargs)"
  install_backends
  install_agent
else
  install_master
fi
log "完成。"
