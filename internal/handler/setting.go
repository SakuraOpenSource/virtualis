package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/SakuraOpenSource/virtualis/internal/service"
)

// Settings returns site-level settings.
func (h *Handler) Settings(c *gin.Context) {
	OK(c, h.settings().Site())
}

// UpdateSettings persists site-level settings.
func (h *Handler) UpdateSettings(c *gin.Context) {
	var req service.SiteConfig
	if !bindJSON(c, &req) {
		return
	}
	out, err := h.settings().SaveSite(req)
	respond(c, out, err)
}

// VirtualisSettings returns virtualization defaults.
func (h *Handler) VirtualisSettings(c *gin.Context) {
	OK(c, h.settings().Virtualis())
}

// UpdateVirtualisSettings persists virtualization defaults.
func (h *Handler) UpdateVirtualisSettings(c *gin.Context) {
	var req service.VirtualisSettings
	if !bindJSON(c, &req) {
		return
	}
	out, err := h.settings().SaveVirtualis(req)
	respond(c, out, err)
}

// CaptchaSettings returns captcha feature flags.
func (h *Handler) CaptchaSettings(c *gin.Context) {
	OK(c, h.settings().Captcha())
}

// UpdateCaptchaSettings persists captcha feature flags.
func (h *Handler) UpdateCaptchaSettings(c *gin.Context) {
	var req service.CaptchaConfig
	if !bindJSON(c, &req) {
		return
	}
	out, err := h.settings().SaveCaptcha(req)
	respond(c, out, err)
}
