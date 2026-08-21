package handler

import "github.com/gin-gonic/gin"

// Captcha issues a new challenge.
func (h *Handler) Captcha(c *gin.Context) {
	ch, err := h.captchaSvc().Issue()
	respond(c, ch, err)
}
