package handler

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/SakuraOpenSource/virtualis/internal/httpx"
	"github.com/SakuraOpenSource/virtualis/internal/model"
)

type createAgentReq struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
}

func (h *Handler) Agents(c *gin.Context) {
	h.agents().MarkOfflineIfStale(90 * time.Second)
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
	respond(c, h.agentSetup(c, agent, token), nil)
}

// AgentHostNetwork 返回被控主机的网卡与地址清单，供独立 IP 模式选择
// 挂载接口与判断该节点是否满足条件。
func (h *Handler) AgentHostNetwork(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	summary, err := h.virtualis().AgentHostNetwork(c.Request.Context(), id)
	respond(c, summary, err)
}

func (h *Handler) RotateAgentToken(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	agent, token, err := h.agents().RotateToken(id)
	if err != nil {
		respond(c, nil, err)
		return
	}
	respond(c, h.agentSetup(c, agent, token), nil)
}

func (h *Handler) agentSetup(c *gin.Context, agent *model.Agent, token string) gin.H {
	master := c.Request.Host
	scheme := "http"
	if c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	masterURL := scheme + "://" + master
	joinCmd := "sudo ./virtualis-agent --master " + shellQuote(masterURL) + " --token " + shellQuote(token) + " --name " + shellQuote(agent.Name)
	curlCmd := "curl -fsSL " + shellQuote(masterURL+"/api/agent/install.sh") + " | bash -s -- --master " + shellQuote(masterURL) + " --token " + shellQuote(token) + " --name " + shellQuote(agent.Name)
	return gin.H{
		"agent":     agent,
		"token":     token,
		"join_cmd":  joinCmd,
		"curl_cmd":  curlCmd,
		"downloads": agentDownloads(masterURL + "/api/agent/binary"),
	}
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

// AgentRegister is called by an agent for its initial registration and heartbeat.
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
		IP       string   `json:"ip"`
		Endpoint string   `json:"endpoint"`
		Driver   string   `json:"driver"`
		Drivers  []string `json:"drivers"`
		OS       string   `json:"os"`
		Arch     string   `json:"arch"`
		Version  string   `json:"version"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "invalid registration body")
		return
	}
	if req.IP == "" {
		req.IP = c.ClientIP()
	}
	if err := h.agents().Heartbeat(agent, token, req.IP, req.Endpoint, req.Driver, req.OS, req.Arch, req.Version, req.Drivers); err != nil {
		respond(c, nil, err)
		return
	}
	updated, err := h.agents().Get(agent.ID)
	if err != nil {
		respond(c, nil, err)
		return
	}
	OK(c, gin.H{"ok": true, "agent": updated})
}

// AgentBinary serves the platform-specific agent packages built into the master.
func (h *Handler) AgentBinary(c *gin.Context) {
	goos := strings.ToLower(strings.TrimSpace(c.Query("os")))
	if goos == "" {
		goos = strings.ToLower(strings.TrimSpace(c.Query("goos")))
	}
	arch := strings.ToLower(strings.TrimSpace(c.Query("arch")))
	if arch == "" {
		arch = strings.ToLower(strings.TrimSpace(c.Query("goarch")))
	}
	if goos == "" {
		goos = "linux"
	}
	if arch == "" {
		arch = "amd64"
	}
	if !validAgentTarget(goos, arch) {
		httpx.NotFound(c, "不支持的被控平台")
		return
	}
	suffix := ""
	if goos == "windows" {
		suffix = ".exe"
	}
	filename := "virtualis-agent-" + goos + "-" + arch + suffix
	candidates := []string{
		filepath.Join("agent-packages", filename),
		filepath.Join("bin", filename),
		filepath.Join("/opt/virtualis", "agent-packages", filename),
		filepath.Join(h.rt.DataDir(), "agent-packages", filename),
		filepath.Join("..", "virtualis-agent", "bin", filename),
		filepath.Join("/usr/local/share/virtualis/agent-packages", filename),
		filepath.Join("/usr/local/bin", filename),
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates, filepath.Join(dir, "agent-packages", filename), filepath.Join(dir, "bin", filename))
	}
	for _, candidate := range candidates {
		st, err := os.Stat(candidate)
		if err != nil || st.IsDir() || st.Size() <= 1024 {
			continue
		}
		c.Header("Content-Description", "File Transfer")
		c.Header("Content-Type", "application/octet-stream")
		c.Header("Content-Disposition", "attachment; filename="+filename)
		http.ServeFile(c.Writer, c.Request, candidate)
		return
	}
	httpx.NotFound(c, "主控未安装该平台的被控安装包，请先运行 build_virtualis.sh --all")
}

func validAgentTarget(goos, arch string) bool {
	if goos != "linux" && goos != "darwin" && goos != "windows" {
		return false
	}
	if arch != "amd64" && arch != "arm64" {
		return false
	}
	return goos != "windows" || arch == "amd64"
}

func agentDownloads(base string) []gin.H {
	items := make([]gin.H, 0, 5)
	for _, item := range []struct{ OS, Arch string }{
		{OS: "linux", Arch: "amd64"},
		{OS: "linux", Arch: "arm64"},
		{OS: "darwin", Arch: "amd64"},
		{OS: "darwin", Arch: "arm64"},
		{OS: "windows", Arch: "amd64"},
	} {
		items = append(items, gin.H{"os": item.OS, "arch": item.Arch, "url": base + "?os=" + item.OS + "&arch=" + item.Arch})
	}
	return items
}

func (h *Handler) AgentInstallScript(c *gin.Context) {
	master := c.Query("master")
	token := c.Query("token")
	if master == "" {
		scheme := "http"
		if c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https" {
			scheme = "https"
		}
		master = scheme + "://" + c.Request.Host
	}
	c.Header("Content-Type", "text/plain; charset=utf-8")
	const script = `#!/usr/bin/env bash
set -Eeuo pipefail

MASTER=%s
TOKEN=%s
NAME=""
MODE=""
ADVERTISE=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --master) MASTER="${2:-}"; shift 2;;
    --token) TOKEN="${2:-}"; shift 2;;
    --name) NAME="${2:-}"; shift 2;;
    --mode) MODE="${2:-}"; shift 2;;
    --advertise) ADVERTISE="${2:-}"; shift 2;;
    *) shift;;
  esac
done

if [[ -z "$MASTER" || -z "$TOKEN" ]]; then
  echo "用法: $0 --master http://MASTER:8080 --token TOKEN [--name node-01] [--mode 1-3]"
  exit 1
fi
NAME="${NAME:-node-$(hostname 2>/dev/null || true)}"
NAME="${NAME:-agent}"

if [[ -z "$MODE" ]]; then
  echo "选择被控后端："
  echo "  1) 仅安装 Agent"
  echo "  2) 安装 Incus + Agent"
  echo "  3) 安装 QEMU + Agent"
  if [[ -t 0 ]]; then read -r -p "选择 [1]: " MODE; else read -r -p "选择 [1]: " MODE < /dev/tty || MODE=1; fi
  MODE="${MODE:-1}"
fi

run_root() {
  if [[ "$(id -u)" -eq 0 ]]; then "$@"; else sudo "$@"; fi
}
install_backend() {
  [[ "$(uname -s 2>/dev/null || true)" == "Linux" ]] || { [[ "$1" == "1" || "$1" == "5" ]] || echo "macOS/Windows 请手动安装所选后端"; return; }
  case "$1" in
    1) echo "跳过额外后端安装";;
    2) if command -v apt-get >/dev/null 2>&1; then run_root apt-get update; run_root apt-get install -y incus; elif command -v dnf >/dev/null 2>&1; then run_root dnf install -y incus; else echo "请手动安装 Incus"; fi;;
    3) if command -v apt-get >/dev/null 2>&1; then run_root apt-get update; run_root apt-get install -y qemu-kvm qemu-utils libvirt-clients libvirt-daemon-system; elif command -v dnf >/dev/null 2>&1; then run_root dnf install -y qemu-kvm qemu-img libvirt; else echo "请手动安装 QEMU/libvirt"; fi;;
    *) echo "未知模式 $1，按仅 Agent 处理";;
  esac
}
install_backend "$MODE"

UNAME_S="$(uname -s 2>/dev/null || true)"
UNAME_M="$(uname -m 2>/dev/null || true)"
case "$UNAME_S" in
  Darwin) GOOS=darwin;;
  MINGW*|MSYS*|CYGWIN*) GOOS=windows;;
  *) GOOS=linux;;
esac
case "$UNAME_M" in
  x86_64|amd64) GOARCH=amd64;;
  arm64|aarch64) GOARCH=arm64;;
  *) echo "不支持的 CPU 架构: $UNAME_M"; exit 1;;
esac
if [[ "$GOOS" == "windows" && "$GOARCH" != "amd64" ]]; then echo "Windows 被控仅提供 amd64"; exit 1; fi
SUFFIX=""; [[ "$GOOS" == "windows" ]] && SUFFIX=.exe
URL="$MASTER/api/agent/binary?os=$GOOS&arch=$GOARCH"
TMP="${TMPDIR:-/tmp}/virtualis-agent.$$${SUFFIX}"
trap 'rm -f "$TMP"' EXIT
echo "从主控下载被控安装包: $GOOS/$GOARCH"
if command -v curl >/dev/null 2>&1; then curl --fail --silent --show-error --location "$URL" -o "$TMP"; elif command -v wget >/dev/null 2>&1; then wget -qO "$TMP" "$URL"; else echo "需要 curl 或 wget"; exit 1; fi
chmod +x "$TMP" 2>/dev/null || true
MAGIC="$(od -An -tx1 -N4 "$TMP" 2>/dev/null | tr -d ' \n')"
if [[ "$GOOS" == "windows" ]]; then [[ "$MAGIC" == 4d5a ]] || { echo "主控返回的不是有效 Windows 安装包"; exit 1; }; else [[ "$MAGIC" == 7f454c46 ]] || { echo "主控返回的不是有效 Unix 安装包"; exit 1; }; fi

if [[ "$GOOS" == "linux" ]]; then
  DEST=/usr/local/bin/virtualis-agent
  run_root install -m 755 "$TMP" "$DEST"
  if command -v systemctl >/dev/null 2>&1; then
    run_root mkdir -p /etc/systemd/system
    if [[ -n "$ADVERTISE" ]]; then ADV_ARG=" --advertise $ADVERTISE"; else ADV_ARG=""; fi
    SERVICE="[Unit]
Description=Virtualis Agent
After=network-online.target

[Service]
ExecStart=$DEST --master $MASTER --token $TOKEN --name $NAME$ADV_ARG
Restart=always
RestartSec=5
User=root

[Install]
WantedBy=multi-user.target
"
    if [[ "$(id -u)" -eq 0 ]]; then printf '%%s\n' "$SERVICE" > /etc/systemd/system/virtualis-agent.service; else printf '%%s\n' "$SERVICE" | sudo tee /etc/systemd/system/virtualis-agent.service >/dev/null; fi
    run_root systemctl daemon-reload
    run_root systemctl enable --now virtualis-agent
  fi
elif [[ "$GOOS" == "darwin" ]]; then
  DEST=/usr/local/bin/virtualis-agent
  run_root install -m 755 "$TMP" "$DEST"
else
  DEST="${ProgramFiles:-C:/Program Files}/Virtualis/virtualis-agent.exe"
  mkdir -p "$(dirname "$DEST")" 2>/dev/null || true
  cp "$TMP" "$DEST"
fi
echo "被控安装完成: $DEST"
echo "主控: $MASTER"
echo "名称: $NAME"
if [[ "$GOOS" != "linux" || ! -x "$(command -v systemctl 2>/dev/null || true)" ]]; then echo "运行: $DEST --master $MASTER --token $TOKEN --name $NAME"; fi
`
	c.String(http.StatusOK, fmt.Sprintf(script, shellQuote(master), shellQuote(token)))
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
