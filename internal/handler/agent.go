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
	// 返回一个可直接执行的 bash 安装脚本，内含 master 与 token
	master := c.Query("master")
	token := c.Query("token")
	if master == "" {
		scheme := "http"
		if c.Request.TLS != nil {
			scheme = "https"
		}
		master = scheme + "://" + c.Request.Host
	}
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.String(http.StatusOK, `#!/usr/bin/env bash
set -e
MASTER="%s"
TOKEN="%s"
echo "Joining $MASTER ..."
curl -L -o /tmp/virtualis-agent https://github.com/SakuraOpenSource/virtualis/releases/latest/download/virtualis-agent-linux-amd64
chmod +x /tmp/virtualis-agent
sudo /tmp/virtualis-agent --master "$MASTER" --token "$TOKEN" --name "node-$(hostname)"
`, master, token)
}
