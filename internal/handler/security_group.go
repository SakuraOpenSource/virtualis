package handler

import (
	"github.com/SakuraOpenSource/virtualis/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *Handler) SecurityGroups(c *gin.Context) {
	items, err := h.virtualis().ListSecurityGroups()
	respond(c, gin.H{"items": items}, err)
}
func (h *Handler) CreateSecurityGroup(c *gin.Context) {
	var in service.SecurityGroupInput
	if !bindJSON(c, &in) {
		return
	}
	item, err := h.virtualis().CreateSecurityGroup(in)
	respond(c, item, err)
}

func (h *Handler) InstanceSecurityGroups(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	item, err := h.virtualis().InstanceSecurityGroups(id)
	respond(c, item, err)
}

func (h *Handler) SetInstanceSecurityGroups(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	var in struct {
		IDs []uint `json:"security_group_ids"`
	}
	if !bindJSON(c, &in) {
		return
	}
	item, err := h.virtualis().SetInstanceSecurityGroups(c.Request.Context(), id, in.IDs)
	respond(c, item, err)
}

func (h *Handler) SecurityGroup(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	item, err := h.virtualis().GetSecurityGroup(id)
	respond(c, item, err)
}
func (h *Handler) UpdateSecurityGroup(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	var in service.SecurityGroupPatch
	if !bindJSON(c, &in) {
		return
	}
	item, err := h.virtualis().UpdateSecurityGroup(c.Request.Context(), id, in)
	respond(c, item, err)
}
func (h *Handler) ReplaceSecurityGroupRules(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	var in struct {
		Rules []service.FirewallInput `json:"rules"`
	}
	if !bindJSON(c, &in) {
		return
	}
	item, err := h.virtualis().ReplaceSecurityGroupRules(c.Request.Context(), id, in.Rules)
	respond(c, item, err)
}
func (h *Handler) DeleteSecurityGroup(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	if err := h.virtualis().DeleteSecurityGroup(id); err != nil {
		respond(c, nil, err)
		return
	}
	noContent(c)
}
