#!/usr/bin/env bash
set -e
# Virtualis 被控一键安装（可选择 QEMU/LXC/Incus/Mock）
# 用法: sudo bash install-agent.sh --master http://MASTER:8080 --token <token> [--name node-01]
# 或交互式: sudo bash install-agent.sh

MASTER=""; TOKEN=""; NAME=""; DRIVERS=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --master) MASTER="$2"; shift 2;;
    --token) TOKEN="$2"; shift 2;;
    --name) NAME="$2"; shift 2;;
    --drivers) DRIVERS="$2"; shift 2;;
    *) shift;;
  esac
done

if [[ -z "$MASTER" || -z "$TOKEN" ]]; then
  echo "=== Virtualis 被控一键安装 ==="
  read -p "主控地址 (如 http://114.66.41.15:8080): " MASTER
  read -p "接入 Token: " TOKEN
  read -p "被控名称 [node-$(hostname)]: " NAME
  NAME=${NAME:-node-$(hostname)}
fi

echo "主控: $MASTER"
echo "名称: $NAME"

if [[ -z "$DRIVERS" ]]; then
  echo ""
  echo "选择虚拟化后端 (可多选，空格分隔):"
  echo "  1) Mock   - 模拟"
  echo "  2) QEMU   - qemu-kvm"
  echo "  3) LXC    - lxc"
  echo "  4) Incus  - incus"
  read -p "选择 [1]: " sel
  sel=${sel:-1}
  DRIVERS="$sel"
fi

# 复用主控的 Linux 安装逻辑，但强制 --agent
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
if [[ -f "$SCRIPT_DIR/install-linux.sh" ]]; then
  echo "调用 install-linux.sh --agent ..."
  bash "$SCRIPT_DIR/install-linux.sh" --agent --master "$MASTER" --token "$TOKEN" --name "$NAME" --drivers "$DRIVERS"
else
  # 独立逻辑：安装所选后端
  echo "安装所选后端: $DRIVERS"
  for s in $DRIVERS; do
    case "$s" in
      1) echo "Mock 无需安装";;
      2) echo "安装 QEMU..."; apt-get update && apt-get install -y qemu-kvm qemu-utils 2>/dev/null || yum install -y qemu-kvm 2>/dev/null || true;;
      3) echo "安装 LXC..."; apt-get install -y lxc 2>/dev/null || true;;
      4) echo "安装 Incus..."; apt-get install -y incus 2>/dev/null || true;;
    esac
  done
  # 下载并启动 agent
  echo "下载 virtualis-agent..."
  curl -fsSL "$MASTER/api/agent/install.sh" | bash -s -- --master "$MASTER" --token "$TOKEN" --name "$NAME"
fi
