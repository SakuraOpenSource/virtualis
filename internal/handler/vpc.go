package handler

import (
	"github.com/SakuraOpenSource/virtualis/internal/service"
	"github.com/gin-gonic/gin"
	"strconv"
)

func (h *Handler) VPCs(c *gin.Context) {
	var agentID uint64
	var err error
	if raw := c.Query("agent_id"); raw != "" {
		agentID, err = strconv.ParseUint(raw, 10, 32)
		if err != nil {
			BadRequest(c, "invalid agent_id")
			return
		}
	}
	items, err := h.virtualis().ListVPCs(uint(agentID))
	respond(c, gin.H{"items": items}, err)
}

func (h *Handler) CreateVPC(c *gin.Context) {
	var req service.VPCInput
	if !bindJSON(c, &req) {
		return
	}
	item, err := h.virtualis().CreateVPC(c.Request.Context(), req)
	respond(c, item, err)
}

func (h *Handler) DeleteVPC(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	if err := h.virtualis().DeleteVPC(c.Request.Context(), id); err != nil {
		respond(c, nil, err)
		return
	}
	noContent(c)
}
