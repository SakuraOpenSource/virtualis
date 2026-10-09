package handler

import (
	"github.com/SakuraOpenSource/virtualis/internal/service"
	"github.com/gin-gonic/gin"
)

// levisOperationIDHeader is the correlation id the Levis plugin attaches to
// its /api/v1 requests (cross-repo contract). The master persists it with the
// operation logs so an upstream change/recovery request can be matched to the
// master-side lifecycle history.
const levisOperationIDHeader = "X-Levis-Operation-ID"

func (h *Handler) ResizeInstance(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	var req service.ResizeInput
	if !bindJSON(c, &req) {
		return
	}
	req.CallerRef = c.GetHeader(levisOperationIDHeader)
	item, err := h.virtualis().ResizeInstance(c.Request.Context(), id, req)
	respond(c, item, err)
}
func (h *Handler) MigrateInstance(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	var req service.MigrationInput
	if !bindJSON(c, &req) {
		return
	}
	// The header is a durable idempotency key on the master side; routes
	// that accept it must forward it through the service input.
	req.CallerRef = c.GetHeader(levisOperationIDHeader)
	item, err := h.virtualis().MigrateInstance(c.Request.Context(), id, req)
	respond(c, item, err)
}
func (h *Handler) Migrations(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	items, err := h.virtualis().ListMigrations(id)
	if err != nil {
		respond(c, nil, err)
		return
	}
	OK(c, gin.H{"items": items})
}
