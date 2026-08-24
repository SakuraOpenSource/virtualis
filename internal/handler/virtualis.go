package handler

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
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

func (h *Handler) InstanceMetrics(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	metrics, err := h.virtualis().InstanceMetrics(c.Request.Context(), id)
	respond(c, gin.H{"metrics": metrics}, err)
}

func (h *Handler) InstanceNetwork(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	network, err := h.virtualis().InstanceNetwork(c.Request.Context(), id)
	respond(c, gin.H{"network": network}, err)
}

func (h *Handler) InstanceVNC(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	vnc, err := h.virtualis().InstanceVNC(c.Request.Context(), id)
	if err == nil && vnc.Available {
		scheme := "ws"
		if c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https" {
			scheme = "wss"
		}
		vnc.WebURL = scheme + "://" + c.Request.Host + "/api/instances/" + c.Param("id") + "/vnc/ws"
	}
	respond(c, gin.H{"vnc": vnc}, err)
}

// InstanceVNCWebSocket proxies the browser's noVNC WebSocket to the assigned
// agent. The agent token is injected server-side and never exposed to Vue.
func (h *Handler) InstanceVNCWebSocket(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	instance, err := h.virtualis().GetInstance(id)
	if err != nil {
		respond(c, nil, err)
		return
	}
	if instance.Agent == nil || instance.Agent.Endpoint == "" || instance.Agent.Token == "" {
		Conflict(c, "被控节点没有可用的 VNC 连接")
		return
	}
	target, err := url.Parse(instance.Agent.Endpoint)
	if err != nil || target.Host == "" {
		Internal(c, "被控 endpoint 无效")
		return
	}
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.URL.Path = "/api/instances/" + c.Param("id") + "/vnc/ws"
			req.URL.RawPath = ""
			query := req.URL.Query()
			query.Set("token", instance.Agent.Token)
			query.Set("name", instance.Name)
			query.Set("driver", instance.Driver)
			req.URL.RawQuery = query.Encode()
			req.Host = target.Host
			req.Header.Set("X-Agent-Token", instance.Agent.Token)
		},
	}
	proxy.ServeHTTP(c.Writer, c.Request)
}

// Drivers returns available virtualization drivers and their probe status.
func (h *Handler) Drivers(c *gin.Context) {
	OK(c, gin.H{"items": h.virtualis().ListDrivers(c.Request.Context())})
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

// UploadImage stores an image or ISO on the master. The bytes are transferred
// to the selected agent only when an instance is created or reinstalled.
func (h *Handler) UploadImage(c *gin.Context) {
	if err := c.Request.ParseMultipartForm(32 << 20); err != nil {
		BadRequest(c, "invalid multipart form")
		return
	}
	header, err := c.FormFile("file")
	if err != nil {
		BadRequest(c, "请选择镜像文件")
		return
	}
	file, err := header.Open()
	if err != nil {
		Internal(c, "无法读取上传文件")
		return
	}
	defer file.Close()
	req := service.UploadImageRequest{
		Name:      c.PostForm("name"),
		Driver:    c.PostForm("driver"),
		Type:      c.PostForm("type"),
		OSType:    c.PostForm("os_type"),
		OSVersion: c.PostForm("os_version"),
		Arch:      c.PostForm("arch"),
	}
	item, err := h.virtualis().UploadImage(req, header.Filename, file)
	respond(c, item, err)
}

// DownloadImage allows an administrator to verify or reuse an uploaded file.
func (h *Handler) DownloadImage(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	image, err := h.virtualis().GetImage(id)
	if err != nil {
		respond(c, nil, err)
		return
	}
	abs, err := h.storage.Path(image.FilePath)
	if err != nil {
		if os.IsNotExist(err) {
			NotFound(c, "image file not found")
			return
		}
		Internal(c, "image file not readable")
		return
	}
	name := image.OriginalName
	if name == "" {
		name = image.Name
	}
	c.Header("Content-Type", image.MimeType)
	c.FileAttachment(abs, name)
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
