package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/SakuraOpenSource/virtualis/internal/httpx"
)

// Page aliases the pagination envelope from httpx.
type Page = httpx.Page

// Re-export common response helpers so calls inside handler stay concise.
var (
	Fail         = httpx.Fail
	BadRequest   = httpx.BadRequest
	Unauthorized = httpx.Unauthorized
	Forbidden    = httpx.Forbidden
	NotFound     = httpx.NotFound
	Conflict     = httpx.Conflict
	Internal     = httpx.Internal
	OK           = httpx.OK
	Pagination   = httpx.Pagination
	IDParam      = httpx.IDParam
)

// bindJSON decodes JSON body into dst and writes 400 on failure.
func bindJSON(c *gin.Context, dst any) bool {
	if err := c.ShouldBindJSON(dst); err != nil {
		BadRequest(c, "invalid request body")
		return false
	}
	return true
}

// noContent writes 204.
func noContent(c *gin.Context) {
	httpx.NoContent(c)
}
