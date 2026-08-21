package httpx

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/SakuraOpenSource/virtualis/internal/model"
)

// Error codes.
const (
	CodeBadRequest   = "BAD_REQUEST"
	CodeUnauthorized = "UNAUTHORIZED"
	CodeForbidden    = "FORBIDDEN"
	CodeNotFound     = "NOT_FOUND"
	CodeConflict     = "CONFLICT"
	CodeNotInstalled = "NOT_INSTALLED"
	CodeInternal     = "INTERNAL"
)

// ErrorBody is the uniform error response.
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Fail aborts with a JSON error.
func Fail(c *gin.Context, status int, code, msg string) {
	c.AbortWithStatusJSON(status, ErrorBody{Code: code, Message: msg})
}

// BadRequest sends 400.
func BadRequest(c *gin.Context, msg string) {
	Fail(c, http.StatusBadRequest, CodeBadRequest, msg)
}

// Unauthorized sends 401.
func Unauthorized(c *gin.Context, msg string) {
	Fail(c, http.StatusUnauthorized, CodeUnauthorized, msg)
}

// Forbidden sends 403.
func Forbidden(c *gin.Context, msg string) {
	Fail(c, http.StatusForbidden, CodeForbidden, msg)
}

// NotFound sends 404.
func NotFound(c *gin.Context, msg string) {
	Fail(c, http.StatusNotFound, CodeNotFound, msg)
}

// Conflict sends 409.
func Conflict(c *gin.Context, msg string) {
	Fail(c, http.StatusConflict, CodeConflict, msg)
}

// Internal sends 500.
func Internal(c *gin.Context, msg string) {
	if msg == "" {
		msg = "internal server error"
	}
	Fail(c, http.StatusInternalServerError, CodeInternal, msg)
}

// OK sends 200 with payload.
func OK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, data)
}

// NoContent sends 204.
func NoContent(c *gin.Context) {
	c.Status(http.StatusNoContent)
}

// Page is a paginated envelope.
type Page struct {
	Items    any   `json:"items"`
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
}

const (
	defaultPageSize = 20
	maxPageSize     = 100
)

// Pagination parses page and page_size query params.
func Pagination(c *gin.Context) (page, pageSize, offset int) {
	page, _ = strconv.Atoi(c.Query("page"))
	if page < 1 {
		page = 1
	}
	pageSize, _ = strconv.Atoi(c.Query("page_size"))
	if pageSize < 1 {
		pageSize = defaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	offset = (page - 1) * pageSize
	return
}

// IDParam extracts a numeric id from path param.
func IDParam(c *gin.Context, name string) (uint, bool) {
	v, err := strconv.ParseUint(c.Param(name), 10, 64)
	if err != nil || v == 0 {
		BadRequest(c, "invalid id")
		return 0, false
	}
	return uint(v), true
}

// QueryUint parses a uint query value.
func QueryUint(c *gin.Context, key string) uint {
	v, err := strconv.ParseUint(c.Query(key), 10, 64)
	if err != nil {
		return 0
	}
	return uint(v)
}

const (
	ctxUserKey   = "virtualis_user"
	ctxUserIDKey = "virtualis_user_id"
)

// SetUser stores authenticated user in gin context.
func SetUser(c *gin.Context, u *model.User) {
	c.Set(ctxUserKey, u)
	c.Set(ctxUserIDKey, u.ID)
}

// CurrentUser returns the current user or nil.
func CurrentUser(c *gin.Context) *model.User {
	v, ok := c.Get(ctxUserKey)
	if !ok {
		return nil
	}
	u, _ := v.(*model.User)
	return u
}

// CurrentUserID returns current user id or 0.
func CurrentUserID(c *gin.Context) uint {
	v, ok := c.Get(ctxUserIDKey)
	if !ok {
		return 0
	}
	id, _ := v.(uint)
	return id
}
