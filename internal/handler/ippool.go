package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/SakuraOpenSource/virtualis/internal/service"
)

// IPPools lists every agent's dedicated-IP pool with entries.
func (h *Handler) IPPools(c *gin.Context) {
	items, err := h.virtualis().ListIPPools()
	respond(c, gin.H{"items": items}, err)
}

// IPPool returns one agent's pool overview.
func (h *Handler) IPPool(c *gin.Context) {
	agentID, ok := IDParam(c, "agentID")
	if !ok {
		return
	}
	item, err := h.virtualis().IPPoolOverviewForAgent(agentID)
	respond(c, item, err)
}

// SaveIPPool updates (upserts) the pool defaults of one agent.
func (h *Handler) SaveIPPool(c *gin.Context) {
	agentID, ok := IDParam(c, "agentID")
	if !ok {
		return
	}
	var req service.IPPoolInput
	if !bindJSON(c, &req) {
		return
	}
	item, err := h.virtualis().SaveIPPoolDefaults(agentID, req)
	respond(c, item, err)
}

// AddIPPoolEntries bulk-adds addresses to one agent's pool.
func (h *Handler) AddIPPoolEntries(c *gin.Context) {
	agentID, ok := IDParam(c, "agentID")
	if !ok {
		return
	}
	var req service.AddIPPoolEntriesInput
	if !bindJSON(c, &req) {
		return
	}
	result, err := h.virtualis().AddIPPoolEntries(agentID, req)
	respond(c, result, err)
}

// UpdateIPPoolEntry edits one pool entry (note / gateway / prefix / status).
func (h *Handler) UpdateIPPoolEntry(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	var req service.UpdateIPPoolEntryInput
	if !bindJSON(c, &req) {
		return
	}
	item, err := h.virtualis().UpdateIPPoolEntry(id, req)
	respond(c, item, err)
}

// DeleteIPPoolEntry removes one pool entry.
func (h *Handler) DeleteIPPoolEntry(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	if err := h.virtualis().DeleteIPPoolEntry(id); err != nil {
		respond(c, nil, err)
		return
	}
	noContent(c)
}

// FreeIPPoolEntries lists currently allocatable addresses of one agent.
// The instance-create dialog uses it to render the IP picker and to
// auto-generate the network configuration for dedicated-IP instances.
func (h *Handler) FreeIPPoolEntries(c *gin.Context) {
	agentID, ok := IDParam(c, "agentID")
	if !ok {
		return
	}
	items, err := h.virtualis().FreeIPPoolEntries(agentID)
	respond(c, gin.H{"items": items}, err)
}
