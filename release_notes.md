## Virtualis HEAD

### ✨ 新特性
- feat: 镜像下载功能（魔方云/TUNA 预设源）与 Incus 分割镜像支持
- feat: NAT 实例创建时自动注入 guest 网络引导与初始 root 密码
- feat: VNC 中继改为手写双向拷贝并记录字节数与断开原因
- feat: NAT 端口映射管理与 root 密码查看
- feat(agent): 支持重置被控 token 并复用接入命令
- feat(install): 支持 GitHub 代理加速下载

### 🐛 问题修复
- fix: Incus 分割镜像元数据改用 incus.tar.xz（meta.tar.xz 是老 LXC 格式）
- fix: TUNA 构建目录不再二次 URL 编码
- fix: TUNA 目录解析过滤页脚外链
- fix: sqlite 相对路径锚定 -data 目录，杜绝按启动目录分叉数据库
- fix: 状态同步/电源操作合并被控回传的 NAT 地址与 MAC
- fix: 创建实例后回写 network 列的 JSON 序列化
- fix: 注册 401 提示改为可操作的修复指引
- fix: 镜像离线存储到 data 文件夹
- fix: 修复安装脚本下载与管理员登录恢复
- fix(install): Agent 改用独立仓库 SakuraOpenSource/virtualis-agent
- fix(install): 避免 /etc/os-release 覆盖 VERSION 导致下载 URL 异常
- fix(install): 主控无需选择/安装虚拟化后端，修复下载 URL 异常

### ♻️ 重构
- refactor: 移除 LXC 驱动选项（安装脚本/驱动校验/前端选项同步）
- refactor: 移除 Mock 与主控 legacy 驱动，网络模型支持独立 IP

### 📝 文档
- docs: 部署脚本移除 Mock 后端选项

### 📦 其他变更
- chore: 更新嵌入前端（VNC 画布高度修复）

**Full Changelog**: https://github.com/SakuraOpenSource/virtualis/compare/v0.2.1...HEAD
