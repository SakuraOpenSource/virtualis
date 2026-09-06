package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/SakuraOpenSource/virtualis/internal/model"
	"github.com/SakuraOpenSource/virtualis/internal/service"
)

// 本文件是 /api/v1 的接口层 —— 面向机器对机器调用的开放 API。
//
// 与 /api 的差别：认证走站点 API Key（X-Virtualis-Api-Key 头）而非会话
// Cookie，因此不受 CSRF 保护组约束；请求结构沿用 service 层定义，但
// 未指定被控节点时自动挑选第一个在线节点，省去调用方先查节点列表。

// V1CreateInstance 创建实例，agent_id 缺省时自动选择第一个在线节点。
func (h *Handler) V1CreateInstance(c *gin.Context) {
	var req service.CreateInstanceRequest
	if !bindJSON(c, &req) {
		return
	}
	if req.AgentID == nil || *req.AgentID == 0 {
		agents, err := service.NewAgentService(h.db()).List()
		if err != nil {
			respond(c, nil, err)
			return
		}
		for _, agent := range agents {
			if agent.IsOnline() {
				id := agent.ID
				req.AgentID = &id
				break
			}
		}
		if req.AgentID == nil {
			BadRequest(c, "没有在线的被控节点，无法创建实例")
			return
		}
	}
	item, err := h.virtualis().CreateInstance(c.Request.Context(), req)
	respond(c, item, err)
}

// V1Images 返回镜像列表，可选按 driver 过滤（incus / qemu）。
func (h *Handler) V1Images(c *gin.Context) {
	items, err := h.virtualis().ListImages()
	if err != nil {
		respond(c, nil, err)
		return
	}
	driver := c.Query("driver")
	out := make([]model.Image, 0, len(items))
	for _, item := range items {
		if item.Status != "" && item.Status != model.ImageStatusAvailable {
			continue
		}
		if driver != "" && item.Driver != "" && item.Driver != model.DriverAuto && item.Driver != driver {
			continue
		}
		out = append(out, item)
	}
	OK(c, gin.H{"items": out})
}

// V1InstanceAccess 返回实例网络与 SSH 访问信息，供上游插件聚合。
func (h *Handler) V1InstanceAccess(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	instance, err := h.virtualis().GetInstance(id)
	if err != nil {
		respond(c, nil, err)
		return
	}
	if instance.Network.Mode == "" {
		instance.Network.Mode = model.NetworkModeNAT
	}
	ip := instance.ObservedIP
	if ip == "" {
		ip = instance.IP
	}
	sshHost, sshPort := ip, 22
	if instance.Network.Mode == model.NetworkModeNAT {
		sshHost, sshPort = "", 0
		for _, mapping := range instance.NATMappings {
			if mapping.Protocol == "tcp" && mapping.GuestPort == 22 {
				if instance.Agent != nil {
					sshHost = instance.Agent.IP
				}
				sshPort = mapping.HostPort
				break
			}
		}
	}
	OK(c, gin.H{
		"network": gin.H{
			"mode": instance.Network.Mode, "ipv4": ip, "mac": instance.Network.MAC,
			"gateway": instance.Network.Gateway, "dns": instance.Network.DNS,
		},
		"ssh": gin.H{
			"host": sshHost, "port": sshPort, "username": "root",
			"password": instance.SSHPassword, "ready": instance.SSHReady,
		},
	})
}
