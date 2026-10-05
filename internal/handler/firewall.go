package handler

import (
	"github.com/SakuraOpenSource/virtualis/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *Handler) Firewall(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	items, err := h.virtualis().ListFirewall(id)
	respond(c, gin.H{"items": items}, err)
}

func (h *Handler) CreateFirewall(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	var req service.FirewallInput
	if !bindJSON(c, &req) {
		return
	}
	item, err := h.virtualis().CreateFirewall(c.Request.Context(), id, req)
	respond(c, item, err)
}

func (h *Handler) UpdateFirewall(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	var req service.FirewallInput
	if !bindJSON(c, &req) {
		return
	}
	item, err := h.virtualis().UpdateFirewall(c.Request.Context(), id, req)
	respond(c, item, err)
}

func (h *Handler) DeleteFirewall(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	if err := h.virtualis().DeleteFirewall(c.Request.Context(), id); err != nil {
		respond(c, nil, err)
		return
	}
	noContent(c)
}
