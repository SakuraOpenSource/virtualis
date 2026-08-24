# Virtualis 部署

本目录提供一键部署脚本，支持选择虚拟化后端并区分主控/被控。

## 快速开始

### 统一 Linux 安装/升级脚本

`install-virtualis.sh` 安装到固定目录 `/opt/virtualis` 和 `/opt/virtualis-agent`，并创建/更新 systemd 服务：

```bash
# 主控
sudo bash deploy/install-virtualis.sh --role master --backend qemu,incus

# 被控首次安装
sudo bash deploy/install-virtualis.sh \
  --role agent \
  --master http://MASTER_IP:8080 \
  --token JOIN_TOKEN \
  --name node-01 \
  --advertise http://AGENT_IP:8081 \
  --backend qemu,incus

# 已安装节点升级，保留现有 systemd 参数
sudo bash deploy/install-virtualis.sh --role master --update
sudo bash deploy/install-virtualis.sh --role agent --update

# 使用 GitHub 代理加速（可选）
# 交互式会询问代理地址；也可通过参数指定，不需要则回车跳过或传 none
sudo bash deploy/install-virtualis.sh --role master --gh-proxy https://gh-proxy.org
sudo bash deploy/install-virtualis.sh --role agent --master http://MASTER_IP:8080 --token TOKEN --name node-01 --gh-proxy https://gh-proxy.org
# 实际下载地址会变为 https://gh-proxy.org/https://github.com/SakuraOpenSource/virtualis/releases/...
```

服务名分别为 `virtualis.service` 和 `virtualis-agent.service`。二进制分别位于 `/opt/virtualis/virtualis` 与 `/opt/virtualis-agent/virtualis-agent`，数据目录分别为 `/var/lib/virtualis` 与 `/var/lib/virtualis-agent`。

如果忘记管理员密码，可在主控上执行：

```bash
sudo systemctl stop virtualis
sudo /opt/virtualis/virtualis -data /var/lib/virtualis --reset-password
sudo systemctl start virtualis
```

### Linux
```bash
sudo bash deploy/install-linux.sh          # 主控
sudo bash deploy/install-linux.sh --agent  # 被控
```

### macOS
```bash
bash deploy/install-macos.sh
bash deploy/install-macos.sh --agent
```

### Windows (管理员 PowerShell/CMD)
```bat
deploy\install.bat
deploy\install.bat --agent
```

脚本会交互式询问：

1. **部署角色**：主控（含前端） / 被控（仅 Go 后端）
2. **虚拟化后端**：可多选 `Mock/QEMU/LXC/Incus`
   - `Mock` 无需依赖，适合演示
   - `QEMU` 需 `qemu-kvm` / `libvirt`
   - `LXC` 需 `lxc`
   - `Incus` 推荐，同时支持容器与虚拟机（需 `incus`）

随后自动安装所选后端、构建 Virtualis（若在源码目录则本地编译，否则从 GitHub Releases 下载）、写入 systemd/launchd/Windows 服务并启动。

## 主控-被控

- **被控**：仅 Go 后端，无前端。`deploy/install-*.sh --agent` 或直接运行 `virtualis-agent` 二进制。
- **添加被控**：在主控 Web 控制台 `被控节点 → 添加被控` 输入名称，生成两条指令：
  - `sudo ./virtualis-agent --master http://MASTER:8080 --token <token> --name node-01`
  - `curl -fsSL http://MASTER:8080/api/agent/install.sh | bash -s -- --master http://MASTER:8080 --token <token>`
- 在被控机器上以 root 执行任一指令，自动注册并心跳（30s）到主控，主控侧状态变为 `online`。

## 手动

- 主控：`virtualis -data /var/lib/virtualis` → 访问 `http://IP:8080` 完成安装
- 被控：`virtualis-agent --master http://MASTER:8080 --token <token> --name node-01 --listen :8081`

## 一键安装 QEMU/LXC/Incus/Mock 被控脚本在哪里

- **被控专用（已整合至 Agent）**：`virtualis-agent/install.sh` – 内置可选，交互式 5 选 1 后直接接入主控
  ```bash
  sudo bash virtualis-agent/install.sh --master http://MASTER:8080 --token <token> --name node-01 --mode 2
  # 或交互式（提示 1 仅 Agent / 2 Incus+Agent / 3 LXC+Agent / 4 QEMU+Agent / 5 Mock+Agent）
  sudo bash virtualis-agent/install.sh
  ```
  可选择：`1 仅安装 Agent / 2 Incus+Agent / 3 LXC+Agent / 4 QEMU+Agent / 5 Mock+Agent`
- **通用**：`deploy/install-linux.sh --agent` / `install-macos.sh --agent` / `install.bat --agent` 仍支持多选 `Mock/QEMU/LXC/Incus`，内部复用同一后端安装逻辑
- 主控也可在 `deploy/install-linux.sh` 直接选后端一并安装

## 目录

- `install-linux.sh` – Linux 主控/被控通用（apt/dnf/yum/pacman/apk）
- `virtualis-agent/install.sh` – Linux 被控专用一键（内置 1 仅Agent / 2 Incus+Agent / 3 LXC+Agent / 4 QEMU+Agent / 5 Mock+Agent）
- `install-macos.sh` – macOS 主控/被控（Homebrew）
- `install.bat` – Windows 主控/被控（winget/choco，QEMU/Mock；LXC/Incus 提示 WSL2）
