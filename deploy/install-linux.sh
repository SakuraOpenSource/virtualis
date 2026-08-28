#!/usr/bin/env bash
set -e
# Virtualis Linux 部署脚本
# 支持选择安装 QEMU / LXC / Incus，并安装 Virtualis 主控或被控
# 用法: sudo bash install-linux.sh  或  bash install-linux.sh --agent

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'

is_root() { [ "$(id -u)" -eq 0 ]; }
pm=""
detect_pm() {
  if command -v apt-get >/dev/null 2>&1; then pm="apt"
  elif command -v dnf >/dev/null 2>&1; then pm="dnf"
  elif command -v yum >/dev/null 2>&1; then pm="yum"
  elif command -v pacman >/dev/null 2>&1; then pm="pacman"
  elif command -v apk >/dev/null 2>&1; then pm="apk"
  elif command -v zypper >/dev/null 2>&1; then pm="zypper"
  else pm="unknown"
  fi
}
install_pkg_apt() { apt-get update && apt-get install -y "$@"; }
install_pkg_dnf() { dnf install -y "$@"; }
install_pkg_yum() { yum install -y "$@"; }
install_pkg_pacman() { pacman -Sy --noconfirm "$@"; }
install_pkg_apk() { apk add "$@"; }

install_pkg() {
  case "$pm" in
    apt) install_pkg_apt "$@" ;;
    dnf) install_pkg_dnf "$@" ;;
    yum) install_pkg_yum "$@" ;;
    pacman) install_pkg_pacman "$@" ;;
    apk) install_pkg_apk "$@" ;;
    *) echo -e "${RED}未知包管理器，请手动安装: $*${NC}"; return 1 ;;
  esac
}

echo -e "${GREEN}=== Virtualis Linux 部署 ===${NC}"
detect_pm
echo "检测到包管理器: $pm"

ROLE="master"
for arg in "$@"; do
  case "$arg" in
    --agent) ROLE="agent" ;;
    --master) ROLE="master" ;;
  esac
done

echo ""
echo "请选择部署角色:"
echo "  1) 主控 (含前端，可管理被控)"
echo "  2) 被控 (仅 Go 后端，无前端)"
read -p "输入 1/2 [1]: " role_choice
if [ "$role_choice" = "2" ] || [ "$ROLE" = "agent" ]; then ROLE="agent"; else ROLE="master"; fi
echo -e "${YELLOW}已选择: $ROLE${NC}"

echo ""
echo "请选择要安装的虚拟化后端 (可多选，空格分隔):"
echo "  1) QEMU   - qemu-kvm / libvirt，适合完整虚拟机"
echo "  2) LXC    - lxc/lxc-templates，适合容器"
echo "  3) Incus  - incus (同时支持容器与虚拟机，推荐)"
echo "  例: 输入 '1 3' 安装 QEMU+Incus，回车默认 1"
read -p "你的选择 [1]: " sel
sel=${sel:-1}

need_qemu=0; need_lxc=0; need_incus=0
for s in $sel; do
  case "$s" in
    1) need_qemu=1 ;;
    2) need_lxc=1 ;;
    3) need_incus=1 ;;
  esac
done

if ! is_root; then
  echo -e "${YELLOW}提示: 未以 root 运行，部分安装可能需要 sudo${NC}"
  SUDO="sudo"
else
  SUDO=""
fi

if [ "$need_qemu" -eq 1 ]; then
  echo -e "${GREEN}安装 QEMU...${NC}"
  case "$pm" in
    apt) $SUDO apt-get update && $SUDO apt-get install -y qemu-kvm qemu-system-x86 qemu-utils libvirt-clients libvirt-daemon-system qemu-img ;;
    dnf|yum) $SUDO install_pkg qemu-kvm qemu-img libvirt ;;
    pacman) $SUDO pacman -Sy --noconfirm qemu libvirt ;;
    apk) $SUDO apk add qemu qemu-img libvirt ;;
  esac
  echo -e "${GREEN}QEMU 安装完成${NC}"
fi

if [ "$need_lxc" -eq 1 ]; then
  echo -e "${GREEN}安装 LXC...${NC}"
  case "$pm" in
    apt) $SUDO apt-get install -y lxc lxc-templates uidmap ;;
    dnf|yum) $SUDO install_pkg lxc lxc-templates ;;
    pacman) $SUDO pacman -Sy --noconfirm lxc ;;
    apk) $SUDO apk add lxc ;;
  esac
fi

if [ "$need_incus" -eq 1 ]; then
  echo -e "${GREEN}安装 Incus...${NC}"
  if command -v incus >/dev/null 2>&1; then
    echo "incus 已安装"
  else
    case "$pm" in
      apt)
        # Incus 官方仓库（Ubuntu/Debian 新版可用 zabbly）
        $SUDO apt-get install -y incus || echo -e "${YELLOW}apt 未找到 incus，请参考 https://linuxcontainers.org/incus/docs/main/install_incus/ 手动安装${NC}"
        ;;
      *) echo -e "${YELLOW}请手动安装 incus: https://linuxcontainers.org/incus/docs/main/install_incus/${NC}" ;;
    esac
  fi
  # 初始化 incus（若未初始化）
  if command -v incus >/dev/null 2>&1; then
    if ! incus info >/dev/null 2>&1; then
      echo "初始化 incus (incus admin init --auto)..."
      $SUDO incus admin init --auto || true
    fi
  fi
fi

echo ""
echo -e "${GREEN}安装 Virtualis ($ROLE)...${NC}"

# 优先从源码构建（若在仓库内），否则下载二进制
if [ -f "go.mod" ] && [ -d "internal" ]; then
  echo "检测到源码，本地构建..."
  if ! command -v go >/dev/null 2>&1; then
    echo -e "${RED}未找到 go，请先安装 Go 1.22+${NC}"; exit 1
  fi
  if [ "$ROLE" = "master" ]; then
    if ! command -v pnpm >/dev/null 2>&1; then
      echo "安装 pnpm..."
      npm install -g pnpm || $SUDO npm install -g pnpm || true
    fi
    if [ -d "../virtualis-frontend" ]; then
      echo "构建前端..."
      (cd ../virtualis-frontend && pnpm install --frozen-lockfile && pnpm build)
      rm -rf internal/web/dist
      mkdir -p internal/web/dist
      cp -R ../virtualis-frontend/dist/. internal/web/dist/
      touch internal/web/dist/.gitkeep
    fi
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /tmp/virtualis ./cmd/virtualis
    $SUDO install -m 755 /tmp/virtualis /usr/local/bin/virtualis
  else
    # 被控仅后端
    mkdir -p agent
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /tmp/virtualis-agent ./cmd/agent 2>/dev/null || CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /tmp/virtualis-agent ./agent/cmd/agent 2>/dev/null || {
      echo "被控源码未找到，尝试构建主控后作为被控使用 (带 --agent 标识)"
      CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /tmp/virtualis-agent ./cmd/virtualis
    }
    $SUDO install -m 755 /tmp/virtualis-agent /usr/local/bin/virtualis-agent
  fi
else
  echo "未找到源码，尝试下载预编译二进制..."
  # 占位：从 GitHub Releases 下载
  VER=${VIRTUALIS_VERSION:-latest}
  URL="https://github.com/SakuraOpenSource/virtualis/releases/${VER}/download/virtualis-linux-amd64"
  if [ "$ROLE" = "agent" ]; then URL="https://github.com/SakuraOpenSource/virtualis/releases/${VER}/download/virtualis-agent-linux-amd64"; fi
  echo "下载 $URL ..."
  curl -L -o /tmp/virtualis "$URL" || wget -O /tmp/virtualis "$URL" || { echo -e "${RED}下载失败，请手动下载${NC}"; exit 1; }
  $SUDO install -m 755 /tmp/virtualis /usr/local/bin/virtualis
fi

# 创建 systemd 服务
if command -v systemctl >/dev/null 2>&1; then
  if [ "$ROLE" = "master" ]; then
    cat | $SUDO tee /etc/systemd/system/virtualis.service >/dev/null <<EOF
[Unit]
Description=Virtualis Master
After=network.target

[Service]
ExecStart=/usr/local/bin/virtualis -data /var/lib/virtualis
Restart=always
User=root
WorkingDirectory=/var/lib/virtualis

[Install]
WantedBy=multi-user.target
EOF
    $SUDO mkdir -p /var/lib/virtualis
    $SUDO systemctl daemon-reload
    $SUDO systemctl enable --now virtualis
    echo -e "${GREEN}主控已启动: systemctl status virtualis${NC}"
  else
    # 被控需要主控地址与 token，通过环境或命令行传入
    echo -e "${YELLOW}被控安装完成，但需从主控生成接入指令${NC}"
    echo "请在主控执行: curl -H \"Authorization: Bearer <admin-token>\" http://master:8080/api/admin/agents -X POST -d '{\"name\":\"node-01\"}'"
    echo "将返回的一键指令在被控机器上执行，例如:"
    echo "  sudo virtualis-agent --master http://MASTER_IP:8080 --token <join-token> --name node-01"
    cat | $SUDO tee /etc/systemd/system/virtualis-agent.service >/dev/null <<EOF
[Unit]
Description=Virtualis Agent
After=network.target

[Service]
# 请手动填入 --master 与 --token
ExecStart=/usr/local/bin/virtualis-agent --master http://MASTER_IP:8080 --token JOIN_TOKEN
Restart=always
User=root

[Install]
WantedBy=multi-user.target
EOF
    echo -e "${YELLOW}已创建 /etc/systemd/system/virtualis-agent.service，请编辑后 systemctl enable --now virtualis-agent${NC}"
  fi
else
  echo -e "${YELLOW}未找到 systemd，请手动运行:${NC}"
  if [ "$ROLE" = "master" ]; then echo "  virtualis -data ./data"; else echo "  virtualis-agent --master http://MASTER:8080 --token TOKEN"; fi
fi

echo ""
echo -e "${GREEN}部署完成！${NC}"
if [ "$ROLE" = "master" ]; then
  echo "访问 http://$(hostname -I | awk '{print $1}'):8080 完成安装"
else
  echo "被控节点安装完成，等待接入主控"
fi
