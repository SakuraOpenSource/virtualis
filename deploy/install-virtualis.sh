#!/usr/bin/env bash
set -Eeuo pipefail

# Virtualis Linux installer/upgrader.
#
# Examples:
#   sudo bash install-virtualis.sh --role master --version latest
#   sudo bash install-virtualis.sh --role agent --master http://10.0.0.1:8080 --token TOKEN --name node-01 --backend qemu,incus
#   sudo bash install-virtualis.sh --role agent --update
#
# The script deliberately installs the two products into separate fixed
# prefixes so upgrading one cannot replace the other:
#   master: /opt/virtualis/virtualis
#   agent:  /opt/virtualis-agent/virtualis-agent

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

MASTER_DIR="/opt/virtualis"
AGENT_DIR="/opt/virtualis-agent"
MASTER_BIN="$MASTER_DIR/virtualis"
MASTER_AGENT_PACKAGES="$MASTER_DIR/agent-packages"
AGENT_BIN="$AGENT_DIR/virtualis-agent"
MASTER_DATA="/var/lib/virtualis"
AGENT_DATA="/var/lib/virtualis-agent"
MASTER_SERVICE="virtualis.service"
AGENT_SERVICE="virtualis-agent.service"
GITHUB_REPO="SakuraOpenSource/virtualis"
VERSION="latest"
ROLE=""
BACKENDS=""
MASTER_URL=""
AGENT_TOKEN=""
AGENT_NAME=""
ADVERTISE=""
LISTEN=":8081"
MASTER_LISTEN=""
SOURCE="auto"
NO_START=0
FORCE=0
DEBUG=0
UPDATE=0

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

die() { printf '%b\n' "${RED}错误: $*${NC}" >&2; exit 1; }
info() { printf '%b\n' "${GREEN}$*${NC}"; }
warn() { printf '%b\n' "${YELLOW}$*${NC}"; }

usage() {
  cat <<'EOF'
Virtualis Linux 安装/升级脚本

用法:
  sudo bash install-virtualis.sh --role master [选项]
  sudo bash install-virtualis.sh --role agent --master URL --token TOKEN --name NAME [选项]

通用选项:
  --role master|agent       安装角色；省略时交互选择
  --version VERSION         latest 或 v1.0.0，默认 latest
  --source auto|release|local
  --backend LIST             mock,qemu,lxc,incus，逗号或空格分隔
  --update                   只升级已安装角色并保留现有配置
  --no-start                 安装后不启动/重启 systemd 服务
  --force                    允许覆盖不存在的旧服务配置
  --debug                    显示更多下载/包管理信息
  -h, --help                显示帮助

主控选项:
  --data DIR                 主控数据目录，默认 /var/lib/virtualis
  --listen ADDR              主控监听地址，默认由已有 config 决定或 :8080

被控选项:
  --master URL               主控地址，例如 http://10.0.0.1:8080
  --token TOKEN              主控生成的被控 token
  --name NAME                被控名称
  --advertise URL            主控可访问的被控地址，例如 http://10.0.0.2:8081
  --listen ADDR              被控 RPC 监听地址，默认 :8081
  --data DIR                 被控数据目录，默认 /var/lib/virtualis-agent

示例:
  sudo bash install-virtualis.sh --role master --backend qemu,incus
  sudo bash install-virtualis.sh --role agent --master http://10.0.0.1:8080 --token TOKEN --name node-01 --backend qemu --advertise http://10.0.0.2:8081
  sudo bash install-virtualis.sh --role agent --update
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --role) ROLE="${2:-}"; shift 2;;
    --version) VERSION="${2:-}"; shift 2;;
    --source) SOURCE="${2:-}"; shift 2;;
    --backend|--backends) BACKENDS="${2:-}"; shift 2;;
    --master) MASTER_URL="${2:-}"; shift 2;;
    --token) AGENT_TOKEN="${2:-}"; shift 2;;
    --name) AGENT_NAME="${2:-}"; shift 2;;
    --advertise) ADVERTISE="${2:-}"; shift 2;;
    --listen) LISTEN="${2:-}"; shift 2;;
    --data)
      if [[ "$ROLE" == "agent" ]]; then AGENT_DATA="${2:-}"; else MASTER_DATA="${2:-}"; fi
      shift 2
      ;;
    --update) SOURCE="release"; UPDATE=1; shift;;
    --no-start) NO_START=1; shift;;
    --force) FORCE=1; shift;;
    --debug) DEBUG=1; shift;;
    -h|--help) usage; exit 0;;
    *) die "未知参数: $1";;
  esac
done

[[ "$(id -u)" -eq 0 ]] || die "请使用 sudo 或 root 执行"
[[ -f /etc/os-release ]] || die "无法识别 Linux 发行版"
# /etc/os-release 会定义 VERSION="13 (trixie)" 等变量，需避免覆盖脚本自身的 VERSION/GITHUB_REPO
_saved_version="$VERSION"
_saved_github_repo="$GITHUB_REPO"
# shellcheck disable=SC1091
. /etc/os-release
VERSION="$_saved_version"
GITHUB_REPO="$_saved_github_repo"

if [[ -z "$ROLE" ]]; then
  echo "请选择安装角色："
  echo "  1) 主控"
  echo "  2) 被控"
  read -r -p "选择 [1]: " role_choice < /dev/tty || role_choice=1
  [[ "$role_choice" == "2" ]] && ROLE="agent" || ROLE="master"
fi
[[ "$ROLE" == "master" || "$ROLE" == "agent" ]] || die "--role 必须是 master 或 agent"

if [[ "$ROLE" == "master" ]]; then
  # 主控无需安装虚拟化后端，实例均在被控创建
  BACKENDS=""
else
  if [[ -z "$BACKENDS" ]]; then
    if [[ "$UPDATE" -eq 1 ]]; then
      BACKENDS="mock"
    else
      echo "请选择虚拟化后端（可多选，空格分隔；回车默认 Mock）："
      echo "  1) Mock"
      echo "  2) QEMU/libvirt"
      echo "  3) LXC"
      echo "  4) Incus"
      read -r -p "选择 [1]: " backend_choice < /dev/tty || backend_choice=1
      backend_choice="${backend_choice:-1}"
      for item in $backend_choice; do
        case "$item" in
          1) BACKENDS+=" mock";;
          2) BACKENDS+=" qemu";;
          3) BACKENDS+=" lxc";;
          4) BACKENDS+=" incus";;
          *) die "未知后端选项: $item";;
        esac
      done
    fi
  fi
  BACKENDS="$(printf '%s' "$BACKENDS" | tr ',' ' ' | xargs)"
  [[ -n "$BACKENDS" ]] || BACKENDS="mock"
fi

command_exists() { command -v "$1" >/dev/null 2>&1; }

if command_exists apt-get; then
  PM="apt"
elif command_exists dnf; then
  PM="dnf"
elif command_exists yum; then
  PM="yum"
elif command_exists pacman; then
  PM="pacman"
elif command_exists apk; then
  PM="apk"
else
  die "不支持的包管理器，请手动安装后端依赖"
fi

pkg_install() {
  case "$PM" in
    apt) DEBIAN_FRONTEND=noninteractive apt-get update; DEBIAN_FRONTEND=noninteractive apt-get install -y "$@";;
    dnf) dnf install -y "$@";;
    yum) yum install -y "$@";;
    pacman) pacman -Sy --noconfirm "$@";;
    apk) apk add "$@";;
  esac
}

install_backend() {
  local backend="$1"
  case "$backend" in
    mock) info "Mock 无需安装额外依赖";;
    qemu)
      info "安装 QEMU/libvirt"
      case "$PM" in
        apt) pkg_install qemu-kvm qemu-utils libvirt-daemon-system libvirt-clients;;
        dnf|yum) pkg_install qemu-kvm qemu-img libvirt libvirt-client;;
        pacman) pkg_install qemu-desktop libvirt;;
        apk) pkg_install qemu-img libvirt qemu-system-x86_64;;
      esac
      systemctl enable --now libvirtd 2>/dev/null || systemctl enable --now libvirt 2>/dev/null || true
      ;;
    lxc)
      info "安装 LXC"
      case "$PM" in
        apt) pkg_install lxc lxc-templates uidmap;;
        dnf|yum) pkg_install lxc lxc-templates;;
        pacman) pkg_install lxc;;
        apk) pkg_install lxc;;
      esac
      ;;
    incus)
      info "安装 Incus"
      case "$PM" in
        apt) pkg_install incus || warn "当前软件源没有 Incus，请按官方文档配置仓库后重跑";;
        dnf|yum) pkg_install incus || warn "当前软件源没有 Incus，请按官方文档配置仓库后重跑";;
        pacman) pkg_install incus;;
        apk) warn "Alpine 请按 Incus 官方文档手动安装";;
      esac
      if command_exists incus && ! incus info >/dev/null 2>&1; then
        incus admin init --auto || true
      fi
      ;;
    *) die "不支持的后端: $backend";;
  esac
}

if [[ "$ROLE" == "agent" ]]; then
  for backend in $BACKENDS; do install_backend "$backend"; done
else
  info "主控无需安装虚拟化后端"
fi

arch_name() {
  local machine
  machine="$(uname -m 2>/dev/null | tr -d '\r\n' | xargs)"
  case "$machine" in
    x86_64|amd64) echo amd64;;
    aarch64|arm64) echo arm64;;
    *) die "不支持的 CPU 架构: $machine";;
  esac
}

download() {
  local url="$1" output="$2"
  url="$(printf '%s' "$url" | tr -d '\r\n' | xargs)"
  [[ -n "$url" ]] || die "下载地址为空，请检查 VERSION/GITHUB_REPO 参数"
  if [[ "$DEBUG" -eq 1 ]]; then info "下载 $url"; fi
  if command_exists curl; then
    curl --fail --location --silent --show-error "$url" -o "$output" || die "下载失败: $url (请检查网络或该版本是否存在 Release 产物)"
  elif command_exists wget; then
    wget --quiet --output-document="$output" "$url" || die "下载失败: $url"
  else
    die "需要 curl 或 wget"
  fi
  [[ -s "$output" ]] || die "下载得到空文件: $url"
}

binary_from_release() {
  local name="$1" output="$2" arch version repo
  arch="$(arch_name)"
  version="$(printf '%s' "$VERSION" | tr -d '\r\n' | xargs)"
  repo="$(printf '%s' "$GITHUB_REPO" | tr -d '\r\n' | xargs)"
  [[ -n "$arch" && -n "$version" && -n "$repo" ]] || die "下载参数异常: version=$version repo=$repo arch=$arch"
  if [[ "$version" == "latest" ]]; then
    download "https://github.com/$repo/releases/latest/download/$name-linux-$arch" "$output"
  else
    download "https://github.com/$repo/releases/download/$version/$name-linux-$arch" "$output"
  fi
}

build_local_master() {
  [[ -f "$REPO_ROOT/go.mod" ]] || return 1
  command_exists go || return 1
  info "从本地源码构建主控"
  mkdir -p "$REPO_ROOT/internal/web/dist"
  if [[ -d "$REPO_ROOT/../virtualis-frontend" && -x "$(command -v pnpm || true)" ]]; then
    (cd "$REPO_ROOT/../virtualis-frontend" && pnpm install --frozen-lockfile && pnpm build)
    rm -rf "$REPO_ROOT/internal/web/dist"
    mkdir -p "$REPO_ROOT/internal/web/dist"
    cp -R "$REPO_ROOT/../virtualis-frontend/dist/." "$REPO_ROOT/internal/web/dist/"
  fi
  (cd "$REPO_ROOT" && CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o "$MASTER_BIN.new" ./cmd/virtualis)
  mv "$MASTER_BIN.new" "$MASTER_BIN"
}

install_master_agent_packages() {
  mkdir -p "$MASTER_AGENT_PACKAGES"
  if [[ "$SOURCE" != "release" && -d "$REPO_ROOT/agent-packages" ]]; then
    find "$REPO_ROOT/agent-packages" -maxdepth 1 -type f -name 'virtualis-agent-*' -exec install -m 0755 {} "$MASTER_AGENT_PACKAGES/" \;
    # 兼容部分旧构建脚本未同步到 agent-packages 的情况
    if [[ ! -f "$MASTER_AGENT_PACKAGES/virtualis-agent-linux-amd64" && -f "$REPO_ROOT/bin/virtualis-agent-linux-amd64" ]]; then
      install -m 0755 "$REPO_ROOT/bin/virtualis-agent-linux-amd64" "$MASTER_AGENT_PACKAGES/virtualis-agent-linux-amd64"
    fi
    if [[ ! -f "$MASTER_AGENT_PACKAGES/virtualis-agent-linux-arm64" && -f "$REPO_ROOT/bin/virtualis-agent-linux-arm64" ]]; then
      install -m 0755 "$REPO_ROOT/bin/virtualis-agent-linux-arm64" "$MASTER_AGENT_PACKAGES/virtualis-agent-linux-arm64"
    fi
    return
  fi
  local package_arch package_tmp repo version
  repo="$(printf '%s' "$GITHUB_REPO" | tr -d '\r\n' | xargs)"
  version="$(printf '%s' "$VERSION" | tr -d '\r\n' | xargs)"
  for package_arch in amd64 arm64; do
    package_tmp="$(mktemp)"
    if [[ "$version" == "latest" ]]; then
      download "https://github.com/$repo/releases/latest/download/virtualis-agent-linux-$package_arch" "$package_tmp"
    else
      download "https://github.com/$repo/releases/download/$version/virtualis-agent-linux-$package_arch" "$package_tmp"
    fi
    install -m 0755 "$package_tmp" "$MASTER_AGENT_PACKAGES/virtualis-agent-linux-$package_arch"
    rm -f "$package_tmp"
  done
}

build_local_agent() {
  [[ -f "$REPO_ROOT/../virtualis-agent/go.mod" ]] || return 1
  command_exists go || return 1
  info "从本地源码构建被控"
  (cd "$REPO_ROOT/../virtualis-agent" && CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o "$AGENT_BIN.new" ./cmd/agent)
  mv "$AGENT_BIN.new" "$AGENT_BIN"
}

install_binary() {
  local role="$1"
  local target source
  if [[ "$role" == "master" ]]; then
    target="$MASTER_BIN"
    if [[ "$SOURCE" != "release" ]] && build_local_master; then
      install_master_agent_packages
      return
    fi
    source="$(mktemp)"
    binary_from_release virtualis "$source"
  else
    target="$AGENT_BIN"
    if [[ "$SOURCE" != "release" ]] && build_local_agent; then return; fi
    source="$(mktemp)"
    binary_from_release virtualis-agent "$source"
  fi
  install -m 0755 "$source" "$target.new"
  mv "$target.new" "$target"
  rm -f "$source"
  [[ "$role" == "master" ]] && install_master_agent_packages
}

write_master_service() {
  local listen_arg=""
  if [[ -n "$MASTER_LISTEN" ]]; then listen_arg=" -listen $MASTER_LISTEN"; fi
  cat > "/etc/systemd/system/$MASTER_SERVICE" <<EOF
[Unit]
Description=Virtualis Master
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
WorkingDirectory=$MASTER_DATA
ExecStart=$MASTER_BIN -data $MASTER_DATA$listen_arg
Restart=always
RestartSec=5
LimitNOFILE=65536
NoNewPrivileges=true
ReadWritePaths=$MASTER_DATA $MASTER_DIR

[Install]
WantedBy=multi-user.target
EOF
}

write_agent_service() {
  [[ -n "$MASTER_URL" ]] || die "被控首次安装必须提供 --master"
  [[ -n "$AGENT_TOKEN" ]] || die "被控首次安装必须提供 --token"
  [[ -n "$AGENT_NAME" ]] || AGENT_NAME="$(hostname -s 2>/dev/null || hostname)"
  local advertise_arg=""
  [[ -n "$ADVERTISE" ]] && advertise_arg=" --advertise $ADVERTISE"
  cat > "/etc/systemd/system/$AGENT_SERVICE" <<EOF
[Unit]
Description=Virtualis Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
WorkingDirectory=$AGENT_DATA
ExecStart=$AGENT_BIN --master $MASTER_URL --token $AGENT_TOKEN --name $AGENT_NAME --listen $LISTEN --data $AGENT_DATA$advertise_arg
Restart=always
RestartSec=5
LimitNOFILE=65536
NoNewPrivileges=false
ReadWritePaths=$AGENT_DATA $AGENT_DIR

[Install]
WantedBy=multi-user.target
EOF
}

install_master() {
  MASTER_LISTEN="${MASTER_LISTEN:-}"
  mkdir -p "$MASTER_DIR" "$MASTER_DATA"
  install_binary master
  write_master_service
  systemctl daemon-reload
  systemctl enable "$MASTER_SERVICE"
  if [[ "$NO_START" -eq 0 ]]; then systemctl restart "$MASTER_SERVICE"; fi
  info "主控已安装: $MASTER_BIN"
  info "内置被控包目录: $MASTER_AGENT_PACKAGES"
  info "数据目录: $MASTER_DATA"
  info "服务: systemctl status $MASTER_SERVICE"
}

install_agent() {
  mkdir -p "$AGENT_DIR" "$AGENT_DATA"
  if [[ -f "/etc/systemd/system/$AGENT_SERVICE" && -z "$MASTER_URL" ]]; then
    # Upgrade path: preserve the existing ExecStart configuration.
    install_binary agent
    systemctl daemon-reload
    systemctl enable "$AGENT_SERVICE"
    if [[ "$NO_START" -eq 0 ]]; then systemctl restart "$AGENT_SERVICE"; fi
  else
    install_binary agent
    write_agent_service
    systemctl daemon-reload
    systemctl enable "$AGENT_SERVICE"
    if [[ "$NO_START" -eq 0 ]]; then systemctl restart "$AGENT_SERVICE"; fi
  fi
  info "被控已安装: $AGENT_BIN"
  info "数据目录: $AGENT_DATA"
  info "服务: systemctl status $AGENT_SERVICE"
}

mkdir -p "$MASTER_DIR" "$AGENT_DIR"
if [[ "$ROLE" == "master" ]]; then
  MASTER_LISTEN="$LISTEN"
  [[ "$MASTER_LISTEN" == ":8081" ]] && MASTER_LISTEN=""
  install_master
else
  install_agent
fi

# 主控无需后端，展示更清晰
display_backend="$BACKENDS"
if [[ "$ROLE" == "master" ]]; then
  display_backend="无需（实例在被控创建）"
elif [[ -z "$display_backend" ]]; then
  display_backend="mock"
fi

cat <<EOF

Virtualis 安装/升级完成
角色: $ROLE
后端: $display_backend
主控目录: $MASTER_DIR
被控目录: $AGENT_DIR
EOF
