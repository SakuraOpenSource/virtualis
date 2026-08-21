package handler

import (
	"net/http"

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

func (h *Handler) AgentInstallScript(c *gin.Context) {
	// 生成兼容两种调用方式的脚本：
	// 1) curl http://master/api/agent/install.sh?master=...&token=... | bash
	// 2) curl http://master/api/agent/install.sh | bash -s -- --master ... --token ...
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
# 支持两种传参：查询串嵌入（兼容）与 bash -s -- --master/--token
MASTER="%s"
TOKEN="%s"
# 解析 bash 传入的 --master/--token
while [[ $# -gt 0 ]]; do
  case "$1" in
    --master) MASTER="$2"; shift 2;;
    --token) TOKEN="$2"; shift 2;;
    --name) NAME="$2"; shift 2;;
    *) shift;;
  esac
done
if [[ -z "$MASTER" || -z "$TOKEN" ]]; then
  echo "用法: $0 --master http://MASTER:8080 --token <token> [--name node-01]"
  echo "或: curl http://MASTER/api/agent/install.sh?master=...&token=... | bash"
  exit 1
fi
NAME=${NAME:-node-$(hostname)}
echo "Joining $MASTER as $NAME ..."
# 优先使用已存在的二进制，否则尝试从主控下载，其次从 GitHub
BIN="/tmp/virtualis-agent"
if [[ -x "./virtualis-agent" ]]; then BIN="./virtualis-agent"; fi
if [[ -x "./va" ]]; then BIN="./va"; fi
if [[ ! -x "$BIN" ]]; then
  echo "尝试从主控下载 agent 二进制..."
  if ! curl -fsSL "$MASTER/api/agent/binary" -o "$BIN" 2>/dev/null; then
    echo "主控未提供二进制，尝试 GitHub Releases..."
    curl -L -o "$BIN" "https://github.com/SakuraOpenSource/virtualis/releases/latest/download/virtualis-agent-linux-amd64" || true
  fi
  chmod +x "$BIN" 2>/dev/null || true
fi
if [[ ! -x "$BIN" ]]; then
  echo "未找到可执行的 virtualis-agent，请先在被控下载或构建："
  echo "  CGO_ENABLED=0 go build -o /tmp/virtualis-agent ./cmd/agent"
  exit 1
fi
exec sudo "$BIN" --master "$MASTER" --token "$TOKEN" --name "$NAME"
`, qMaster, qToken)
}
