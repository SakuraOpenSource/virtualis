package handler

import (
	"fmt"
	"mime"
	"net/http"

	"github.com/SakuraOpenSource/virtualis/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *Handler) Snapshots(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	items, err := h.virtualis().ListSnapshots(id)
	respond(c, gin.H{"items": items}, err)
}
func (h *Handler) CreateSnapshot(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	var req service.RecoveryInput
	if !bindJSON(c, &req) {
		return
	}
	item, err := h.virtualis().CreateSnapshot(c.Request.Context(), id, req)
	respond(c, item, err)
}
func (h *Handler) RestoreSnapshot(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	sid, ok := IDParam(c, "sid")
	if !ok {
		return
	}
	item, err := h.virtualis().RestoreSnapshot(c.Request.Context(), id, sid)
	respond(c, item, err)
}
func (h *Handler) DeleteSnapshot(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	sid, ok := IDParam(c, "sid")
	if !ok {
		return
	}
	if err := h.virtualis().DeleteSnapshot(c.Request.Context(), id, sid); err != nil {
		respond(c, nil, err)
		return
	}
	noContent(c)
}
func (h *Handler) Backups(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	items, err := h.virtualis().ListBackups(id)
	respond(c, gin.H{"items": items}, err)
}
func (h *Handler) CreateBackup(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	var req service.RecoveryInput
	if !bindJSON(c, &req) {
		return
	}
	item, err := h.virtualis().CreateBackup(c.Request.Context(), id, req)
	respond(c, item, err)
}
func (h *Handler) RestoreBackup(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	bid, ok := IDParam(c, "bid")
	if !ok {
		return
	}
	item, err := h.virtualis().RestoreBackup(c.Request.Context(), id, bid)
	respond(c, item, err)
}
func (h *Handler) DeleteBackup(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	bid, ok := IDParam(c, "bid")
	if !ok {
		return
	}
	if err := h.virtualis().DeleteBackup(c.Request.Context(), id, bid); err != nil {
		respond(c, nil, err)
		return
	}
	noContent(c)
}
func (h *Handler) DownloadBackup(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	bid, ok := IDParam(c, "bid")
	if !ok {
		return
	}
	file, backup, err := h.virtualis().OpenBackup(c.Request.Context(), id, bid)
	if err != nil {
		respond(c, nil, err)
		return
	}
	defer file.Close()
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": fmt.Sprintf("%s.%s", backup.Name, backup.Format)}))
	c.Header("X-Checksum-SHA256", backup.Checksum)
	c.DataFromReader(http.StatusOK, backup.SizeBytes, "application/octet-stream", file, nil)
}
