# Virtualis 部署

本目录提供一键部署脚本（Linux / macOS / Windows），统一目录约定：

- 主控：`/opt/virtualis/master`（二进制 + `data/` + `agent-packages/`）
- 被控：`/opt/virtualis/agent`（二进制 + `data/`）

Windows 等价目录：`C:\opt\virtualis\master` 与 `C:\opt\virtualis\agent`。

## 快速开始

### Linux / macOS：install-virtualis.sh

```bash
# 主控（仓库内执行会自动源码构建并嵌入前端；否则拉 GitHub release）
sudo bash deploy/install-virtualis.sh

# 主控（指定 GitHub 代理）
sudo bash deploy/install-virtualis.sh --gh-proxy https://gh-proxy.org

# 被控首次安装（虚拟化后端可交互选择或 --backends qemu,incus）
sudo bash deploy/install-virtualis.sh --agent \
  --master-url http://MASTER_IP:8080 \
  --token JOIN_TOKEN \
  --name node-01 \
  --backends qemu,incus

# 已安装节点升级（保留现有 systemd 参数与数据）
sudo bash deploy/install-virtualis.sh --agent --update
sudo bash deploy/install-virtualis.sh --master --update
```

被控二进制一律从 **GitHub virtualis-agent Releases** 获取最新版；
GitHub 不可达时自动回退主控分发端点 `/api/agent/binary`。

### Windows：install-virtualis.cmd

以管理员身份运行 CMD：

```bat
REM 主控
install-virtualis.cmd

REM 被控
install-virtualis.cmd --agent --master-url http://MASTER:8080 --token TOKEN --name node-01
```

### macOS：install-macos.sh

兼容入口，直接转发到 `install-virtualis.sh`，参数相同。

## systemd 服务

- 主控：`virtualis.service` → `/opt/virtualis/master/virtualis -data /opt/virtualis/master/data`
- 被控：`virtualis-agent.service` → `/opt/virtualis/agent/virtualis-agent --master ... --token ... --data /opt/virtualis/agent/data`

## 被控版本管理

- `--version v1.2.3` 指定 agent release tag；缺省 `latest`
- 主控安装时会把 agent 二进制同步进 `agent-packages/`（优先本地
  `agent-packages/`，否则从 GitHub agent release 下载 linux amd64/arm64）
- 被控端 `/api/agent/binary` 分发端点仅在 GitHub 不可达时作为回退使用
