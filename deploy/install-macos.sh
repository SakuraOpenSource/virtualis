#!/usr/bin/env bash
# ==============================================================================
# Virtualis macOS 一键安装（统一走 install-virtualis.sh，本文件为兼容入口）
#
# 目录约定：
#   /opt/virtualis/master    主控
#   /opt/virtualis/agent     被控
# ==============================================================================
set -e
DIR="$(cd "$(dirname "$0")" && pwd)"
exec bash "$DIR/install-virtualis.sh" "$@"
