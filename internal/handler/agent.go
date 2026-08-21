package handler

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/SakuraOpenSource/virtualis/internal/httpx"
)

type createAgentReq struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
}

func (h *Handler) Agents(c *gin.Context) {
	items, err := h.agents().List()
	respond(c, gin.H{"items": items}, err)
}

func (h *Handler) CreateAgent(c *gin.Context) {
	var req createAgentReq
	if !bindJSON(c, &req) {
		return
	}
	agent, token, err := h.agents().Create(req.Name, req.DisplayName)
	if err != nil {
		respond(c, nil, err)
		return
	}
	// 生成一键接入指令
	master := c.Request.Host
	scheme := "http"
	if c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	joinCmd := "sudo ./virtualis-agent --master " + scheme + "://" + master + " --token " + token + " --name " + agent.Name
	// 同时提供 curl 形式的远程安装指令
	curlCmd := "curl -fsSL " + scheme + "://" + master + "/api/agent/install.sh | bash -s -- --master " + scheme + "://" + master + " --token " + token
	respond(c, gin.H{
		"agent":    agent,
		"token":    token,
		"join_cmd": joinCmd,
		"curl_cmd": curlCmd,
	}, nil)
}

func (h *Handler) DeleteAgent(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	if err := h.agents().Delete(id); err != nil {
		respond(c, nil, err)
		return
	}
	noContent(c)
}

// AgentRegister 供被控调用，使用 token 鉴权
func (h *Handler) AgentRegister(c *gin.Context) {
	token := c.GetHeader("X-Agent-Token")
	if token == "" {
		token = c.Query("token")
	}
	if token == "" {
		httpx.Unauthorized(c, "缺少 token")
		return
	}
	agent, err := h.agents().Authenticate(token)
	if err != nil {
		respond(c, nil, err)
		return
	}
	var req struct {
		IP      string `json:"ip"`
		Driver  string `json:"driver"`
		Version string `json:"version"`
	}
	_ = c.ShouldBindJSON(&req)
	if req.IP == "" {
		req.IP = c.ClientIP()
	}
	if err := h.agents().Heartbeat(agent, req.IP, req.Driver, req.Version); err != nil {
		respond(c, nil, err)
		return
	}
	OK(c, gin.H{"ok": true, "agent": agent})
}

func (h *Handler) AgentBinary(c *gin.Context) {
	// Serve agent binary for download by install.sh
	// Query: ?os=linux&arch=amd64 (default linux/amd64)
	goos := c.Query("os")
	if goos == "" {
		goos = c.Query("goos")
	}
	arch := c.Query("arch")
	if arch == "" {
		arch = c.Query("goarch")
	}
	if goos == "" {
		goos = "linux"
	}
	if arch == "" {
		arch = "amd64"
	}
	// Windows needs .exe
	suffix := ""
	if goos == "windows" {
		suffix = ".exe"
	}
	// Candidate paths (order matters)
	candidates := []string{
		filepath.Join("bin", "virtualis-agent-"+goos+"-"+arch+suffix),
		filepath.Join("bin", "virtualis-agent"+suffix),
		"virtualis-agent" + suffix,
		"virtualis-agent-" + goos + "-" + arch + suffix,
		filepath.Join("..", "virtualis-agent", "bin", "virtualis-agent-"+goos+"-"+arch+suffix),
		filepath.Join("..", "virtualis-agent", "bin", "virtualis-agent"+suffix),
		filepath.Join("/usr/local/bin", "virtualis-agent"+suffix),
		filepath.Join("/usr/local/bin", "virtualis-agent-"+goos+"-"+arch+suffix),
	}
	// Also try absolute near executable
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "virtualis-agent-"+goos+"-"+arch+suffix),
			filepath.Join(dir, "virtualis-agent"+suffix),
			filepath.Join(dir, "bin", "virtualis-agent-"+goos+"-"+arch+suffix),
		)
	}
	var found string
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Size() > 1024 {
			found = p
			break
		}
	}
	if found == "" {
		// No local binary, redirect to GitHub releases as fallback
		// Let install.sh handle fallback, return 404 so curl -f triggers fallback
		httpx.NotFound(c, "agent binary not found on master, please build with build_virtualis.sh --all or check GitHub releases")
		return
	}
	c.Header("Content-Description", "File Transfer")
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", "attachment; filename=virtualis-agent"+suffix)
	c.Header("Content-Transfer-Encoding", "binary")
	c.File(found)
	// Also support HEAD
	if strings.EqualFold(c.Request.Method, "HEAD") {
		c.AbortWithStatus(http.StatusOK)
		return
	}
}

func (h *Handler) AgentInstallScript(c *gin.Context) {
	// 生成兼容两种调用方式的脚本，并内置 5 选 1 后端安装：
	// 1) curl http://master/api/agent/install.sh?master=...&token=... | bash
	// 2) curl http://master/api/agent/install.sh | bash -s -- --master ... --token ... [--mode 1-5] [--name node-01]
	qMaster := c.Query("master")
	qToken := c.Query("token")
	if qMaster == "" {
		scheme := "http"
		if c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https" {
			scheme = "https"
		}
		qMaster = scheme + "://" + c.Request.Host
	}
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.String(http.StatusOK, `#!/usr/bin/env bash
set -e
# 支持两种传参：查询串嵌入（兼容）与 bash -s -- --master/--token/--mode
MASTER="%s"
TOKEN="%s"
MODE=""
NAME=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --master) MASTER="$2"; shift 2;;
    --token) TOKEN="$2"; shift 2;;
    --name) NAME="$2"; shift 2;;
    --mode) MODE="$2"; shift 2;;
    *) shift;;
  esac
done
if [[ -z "$MASTER" || -z "$TOKEN" ]]; then
  echo "用法: $0 --master http://MASTER:8080 --token <token> [--name node-01] [--mode 1-5]"
  echo "或: curl http://MASTER/api/agent/install.sh?master=...&token=... | bash"
  echo "可选择: 1 仅安装 Agent / 2 Incus+Agent / 3 LXC+Agent / 4 QEMU+Agent / 5 Mock+Agent"
  exit 1
fi
NAME=${NAME:-node-$(hostname)}
if [[ -z "$MODE" ]]; then
  echo ""
  echo "可选择："
  echo "  1) 仅安装 Agent"
  echo "  2) 安装 Incus+Agent"
  echo "  3) 安装 LXC+Agent"
  echo "  4) 安装 QEMU+Agent"
  echo "  5) 安装 Mock+Agent"
  if [ -t 0 ]; then
    read -p "选择 [1]: " MODE
  else
    read -p "选择 [1]: " MODE < /dev/tty || MODE=1
  fi
  MODE=${MODE:-1}
fi
echo "Joining $MASTER as $NAME (模式 $MODE) ..."
case "$MODE" in
  1) echo "仅安装 Agent，跳过后端安装" ;;
  2) echo "安装 Incus..."; if command -v apt-get >/dev/null 2>&1; then apt-get update && apt-get install -y incus 2>/dev/null || true; elif command -v dnf >/dev/null 2>&1; then dnf install -y incus 2>/dev/null || true; elif command -v yum >/dev/null 2>&1; then yum install -y incus 2>/dev/null || true; else echo "请手动安装 incus"; fi ;;
  3) echo "安装 LXC..."; if command -v apt-get >/dev/null 2>&1; then apt-get update && apt-get install -y lxc lxc-templates 2>/dev/null || true; elif command -v dnf >/dev/null 2>&1; then dnf install -y lxc lxc-templates 2>/dev/null || true; elif command -v yum >/dev/null 2>&1; then yum install -y lxc lxc-templates 2>/dev/null || true; else echo "请手动安装 lxc"; fi ;;
  4) echo "安装 QEMU..."; if command -v apt-get >/dev/null 2>&1; then apt-get update && apt-get install -y qemu-kvm qemu-utils libvirt-clients 2>/dev/null || true; elif command -v dnf >/dev/null 2>&1; then dnf install -y qemu-kvm qemu-img 2>/dev/null || true; elif command -v yum >/dev/null 2>&1; then yum install -y qemu-kvm qemu-img 2>/dev/null || true; else echo "请手动安装 qemu-kvm"; fi ;;
  5) echo "安装 Mock+Agent (Mock 无需额外依赖)" ;;
  *) echo "未知模式 $MODE，按 1 仅 Agent 处理" ;;
esac
# 检测系统架构，用于下载对应二进制
GOOS="linux"; GOARCH="amd64"
UNAME_S=$(uname -s 2>/dev/null || echo Linux)
UNAME_M=$(uname -m 2>/dev/null || echo x86_64)
case "$UNAME_M" in x86_64|amd64) GOARCH="amd64";; aarch64|arm64) GOARCH="arm64";; armv7*|arm) GOARCH="arm64";; *) GOARCH="amd64";; esac
case "$UNAME_S" in Darwin) GOOS="darwin";; Linux) GOOS="linux";; MINGW*|MSYS*|CYGWIN*) GOOS="windows";; *) GOOS="linux";; esac
is_valid() { [ -f "$1" ] && [ -x "$1" ] && head -c 4 "$1" 2>/dev/null | od -An -tx1 2>/dev/null | grep -q "7f 45 4c 46"; }
# 优先使用已存在的二进制
BIN="/tmp/virtualis-agent"
if [[ -x "./virtualis-agent" ]] && is_valid "./virtualis-agent"; then BIN="./virtualis-agent"; fi
if [[ -x "./va" ]] && is_valid "./va"; then BIN="./va"; fi
if ! is_valid "$BIN"; then
  rm -f "$BIN" 2>/dev/null || true
  echo "尝试从主控下载 agent 二进制 ($GOOS/$GOARCH)..."
  if ! curl -fsSL "$MASTER/api/agent/binary?os=$GOOS&arch=$GOARCH" -o "$BIN" 2>/dev/null; then
    echo "主控未提供二进制，尝试 GitHub Releases..."
    curl -L -o "$BIN" "https://github.com/SakuraOpenSource/virtualis/releases/latest/download/virtualis-agent-${GOOS}-${GOARCH}" 2>/dev/null || true
  fi
  chmod +x "$BIN" 2>/dev/null || true
  if ! is_valid "$BIN"; then
    echo "主控下载的不是有效二进制，尝试 GitHub Releases..."
    rm -f "$BIN" 2>/dev/null || true
    curl -L -o "$BIN" "https://github.com/SakuraOpenSource/virtualis/releases/latest/download/virtualis-agent-${GOOS}-${GOARCH}" 2>/dev/null || true
    chmod +x "$BIN" 2>/dev/null || true
  fi
fi
if ! is_valid "$BIN"; then
  echo "未找到可执行的 virtualis-agent，请先在被控下载或构建："
  echo "  CGO_ENABLED=0 go build -o /tmp/virtualis-agent ./cmd/agent"
  echo "或从主控执行: bash virtualis/build_virtualis.sh --all 后将 bin/ 下二进制放到主控可访问路径"
  exit 1
fi
exec sudo "$BIN" --master "$MASTER" --token "$TOKEN" --name "$NAME"
`, qMaster, qToken)
}
