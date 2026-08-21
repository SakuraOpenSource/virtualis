package handler

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/SakuraOpenSource/virtualis/internal/model"
	"github.com/SakuraOpenSource/virtualis/internal/service"
)

// Instances returns paginated instances.
func (h *Handler) Instances(c *gin.Context) {
	page, pageSize, _ := Pagination(c)
	items, total, err := h.virtualis().ListInstances(page, pageSize)
	if err != nil {
		respond(c, nil, err)
		return
	}
	OK(c, Page{Items: items, Total: total, Page: page, PageSize: pageSize})
}

// Instance returns a single instance.
func (h *Handler) Instance(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	item, err := h.virtualis().GetInstance(id)
	respond(c, item, err)
}

// CreateInstance creates a new virtual instance.
func (h *Handler) CreateInstance(c *gin.Context) {
	var req service.CreateInstanceRequest
	if !bindJSON(c, &req) {
		return
	}
	item, err := h.virtualis().CreateInstance(c.Request.Context(), req)
	respond(c, item, err)
}

// DeleteInstance deletes an instance.
func (h *Handler) DeleteInstance(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	if err := h.virtualis().DeleteInstance(c.Request.Context(), id); err != nil {
		respond(c, nil, err)
		return
	}
	noContent(c)
}

// PowerRequest is body for power operations.
type PowerRequest struct {
	Action  string `json:"action"`
	ImageID *uint  `json:"image_id"`
}

// InstancePower executes power actions.
// Supported actions: start, stop, restart, hard_start, hard_stop, hard_restart, reinstall.
// For reinstall, optional image_id may be provided.
//
// Action strings are normalized: camelCase variants are accepted and
// converted to snake_case before delegating to service.
func (h *Handler) InstancePower(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	var req PowerRequest
	if !bindJSON(c, &req) {
		return
	}
	action := strings.TrimSpace(req.Action)
	if action == "" {
		BadRequest(c, "action required")
		return
	}
	// Normalize camelCase to snake_case for service compatibility.
	action = normalizeAction(action)

	// If reinstall carries an explicit image_id, update the instance record first.
	if action == "reinstall" && req.ImageID != nil {
		// Persist new image association before reinstall so driver picks correct image.
		// We do a direct DB update; failures are reported to caller.
		inst, err := h.virtualis().GetInstance(id)
		if err != nil {
			respond(c, nil, err)
			return
		}
		// Validate image exists.
		if _, err := h.virtualis().GetImage(*req.ImageID); err != nil {
			respond(c, nil, err)
			return
		}
		// Update instance's image_id.
		if inst.ImageID == nil || *inst.ImageID != *req.ImageID {
			if err := h.db().Model(&model.Instance{}).Where("id = ?", id).Update("image_id", req.ImageID).Error; err != nil {
				respond(c, nil, err)
				return
			}
		}
	}

	item, err := h.virtualis().PowerInstance(c.Request.Context(), id, action)
	respond(c, item, err)
}

func normalizeAction(a string) string {
	switch a {
	case "hardStart":
		return "hard_start"
	case "hardStop":
		return "hard_stop"
	case "hardRestart":
		return "hard_restart"
	default:
		return strings.ToLower(a)
	}
}

// InstanceStatus refreshes instance status from driver and returns it.
func (h *Handler) InstanceStatus(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	item, err := h.virtualis().RefreshStatus(c.Request.Context(), id)
	respond(c, item, err)
}

// Drivers returns available virtualization drivers and their probe status.
func (h *Handler) Drivers(c *gin.Context) {
	names := h.virtualis().DriverNames()
	status := h.virtualis().ListDrivers(c.Request.Context())
	// Build slice for frontend convenience.
	type entry struct {
		Name      string `json:"name"`
		Available bool   `json:"available"`
		Error     string `json:"error,omitempty"`
	}
	var out []entry
	for _, n := range names {
		e := entry{Name: n, Available: status[n] == nil}
		if status[n] != nil {
			e.Error = status[n].Error()
		}
		out = append(out, e)
	}
	OK(c, gin.H{"items": out})
}

// Images returns all images.
func (h *Handler) Images(c *gin.Context) {
	items, err := h.virtualis().ListImages()
	if err != nil {
		respond(c, nil, err)
		return
	}
	OK(c, gin.H{"items": items})
}

// Image returns a single image.
func (h *Handler) Image(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	item, err := h.virtualis().GetImage(id)
	respond(c, item, err)
}

// CreateImage creates a new image record.
func (h *Handler) CreateImage(c *gin.Context) {
	var req service.CreateImageRequest
	if !bindJSON(c, &req) {
		return
	}
	item, err := h.virtualis().CreateImage(req)
	respond(c, item, err)
}

// DeleteImage removes an image.
func (h *Handler) DeleteImage(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	if err := h.virtualis().DeleteImage(id); err != nil {
		respond(c, nil, err)
		return
	}
	noContent(c)
}
