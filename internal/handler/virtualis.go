package handler

import (
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

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

// InstanceNATCreate 为实例新增 NAT 端口映射。
func (h *Handler) InstanceNATCreate(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	var req service.CreateNATMappingRequest
	if !bindJSON(c, &req) {
		return
	}
	mapping, err := h.virtualis().CreateNATMapping(c.Request.Context(), id, req)
	respond(c, mapping, err)
}

// InstanceNATDelete 删除实例的一条 NAT 端口映射。
func (h *Handler) InstanceNATDelete(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	mid, err := strconv.ParseUint(c.Param("mid"), 10, 64)
	if err != nil || mid == 0 {
		BadRequest(c, "invalid mapping id")
		return
	}
	err = h.virtualis().DeleteNATMapping(c.Request.Context(), id, uint(mid))
	if err != nil {
		respond(c, nil, err)
		return
	}
	noContent(c)
}

type setPasswordRequest struct {
	Password string `json:"password"`
}

// InstancePasswordSet 设置实例的 root 密码（运行中会异步注入）。
func (h *Handler) InstancePasswordSet(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	var req setPasswordRequest
	if !bindJSON(c, &req) {
		return
	}
	instance, err := h.virtualis().SetInstancePassword(c.Request.Context(), id, req.Password)
	respond(c, instance, err)
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

// vncUpgrader 与被控端保持一致：noVNC 走 binary 子协议；来源不校验，因为
// 鉴权已由 cookie 中间件完成，且前后端分离部署时 Origin 是前端域名。
var vncUpgrader = websocket.Upgrader{
	ReadBufferSize:  32 << 10,
	WriteBufferSize: 32 << 10,
	Subprotocols:    []string{"binary"},
	CheckOrigin:     func(*http.Request) bool { return true },
}

// InstanceVNCWebSocket 把浏览器的 noVNC WebSocket 中继到所属被控。
// 不用 httputil.ReverseProxy：它劫持连接后完全黑盒，"连上又断"这类问题
// 看不出是浏览器→主控还是主控→被控哪一跳先断；手写双向拷贝与被控端
// 日志对称，两个方向的字节数和关闭原因都直接进 journal。
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
	browser, err := vncUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return // 升级失败时响应已写出
	}
	defer browser.Close()
	browser.SetReadLimit(16 << 20)

	agentURL := url.URL{Scheme: "ws", Host: target.Host, Path: "/api/instances/" + c.Param("id") + "/vnc/ws"}
	if target.Scheme == "https" {
		agentURL.Scheme = "wss"
	}
	query := url.Values{}
	query.Set("token", instance.Agent.Token)
	query.Set("name", instance.Name)
	query.Set("driver", instance.Driver)
	agentURL.RawQuery = query.Encode()
	header := http.Header{}
	header.Set("X-Agent-Token", instance.Agent.Token)

	dialer := websocket.Dialer{ReadBufferSize: 32 << 10, WriteBufferSize: 32 << 10, HandshakeTimeout: 10 * time.Second}
	agent, resp, err := dialer.Dial(agentURL.String(), header)
	if err != nil {
		log.Printf("VNC 代理 实例 %d 连接被控 %s 失败: %v", id, target.Host, err)
		_ = browser.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "连接被控节点失败"),
			time.Now().Add(time.Second))
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return
	}
	defer agent.Close()
	log.Printf("VNC 代理 实例 %d 已建立: 浏览器 %s ↔ 被控 %s", id, c.ClientIP(), target.Host)

	// 一侧断开后立即关掉两侧连接，让对向拷贝马上退出而不是等对端超时。
	var bytesToAgent, bytesToBrowser atomic.Uint64
	result := make(chan error, 2)
	go func() { // 浏览器 → 被控
		for {
			messageType, reader, readErr := browser.NextReader()
			if readErr != nil {
				result <- readErr
				return
			}
			writer, writeErr := agent.NextWriter(messageType)
			if writeErr != nil {
				result <- writeErr
				return
			}
			n, copyErr := io.Copy(writer, reader)
			bytesToAgent.Add(uint64(n))
			_ = writer.Close()
			if copyErr != nil {
				result <- copyErr
				return
			}
		}
	}()
	go func() { // 被控 → 浏览器
		for {
			messageType, reader, readErr := agent.NextReader()
			if readErr != nil {
				result <- readErr
				return
			}
			writer, writeErr := browser.NextWriter(messageType)
			if writeErr != nil {
				result <- writeErr
				return
			}
			n, copyErr := io.Copy(writer, reader)
			bytesToBrowser.Add(uint64(n))
			_ = writer.Close()
			if copyErr != nil {
				result <- copyErr
				return
			}
		}
	}()
	closeErr := <-result
	_ = browser.Close()
	_ = agent.Close()
	<-result
	log.Printf("VNC 代理 实例 %d 断开: %v（浏览器→QEMU %d B / QEMU→浏览器 %d B）",
		id, closeErr, bytesToAgent.Load(), bytesToBrowser.Load())
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
