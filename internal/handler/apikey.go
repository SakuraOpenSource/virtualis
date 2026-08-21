package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/SakuraOpenSource/virtualis/internal/httpx"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"github.com/SakuraOpenSource/virtualis/internal/service"
)

// APIKeys lists current user's API keys.
func (h *Handler) APIKeys(c *gin.Context) {
	items, err := h.apiKeys().List(httpx.CurrentUserID(c))
	if err != nil {
		respond(c, nil, err)
		return
	}
	OK(c, gin.H{"items": items, "scopes": model.AllScopes()})
}

// CreateAPIKey creates a new API key for the current user.
func (h *Handler) CreateAPIKey(c *gin.Context) {
	var req service.APIKeyCreateRequest
	if !bindJSON(c, &req) {
		return
	}
	created, err := h.apiKeys().Create(httpx.CurrentUserID(c), req)
	respond(c, created, err)
}

// RevokeAPIKey revokes an API key owned by the current user.
func (h *Handler) RevokeAPIKey(c *gin.Context) {
	id, ok := IDParam(c, "id")
	if !ok {
		return
	}
	if err := h.apiKeys().Revoke(id, httpx.CurrentUserID(c)); err != nil {
		respond(c, nil, err)
		return
	}
	noContent(c)
}
