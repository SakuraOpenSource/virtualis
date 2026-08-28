package service

import (
	"errors"
	"fmt"
	"net/http"
)

// BizError is a business error with HTTP semantics.
type BizError struct {
	Status  int
	Code    string
	Message string
}

func (e *BizError) Error() string { return e.Message }

func newBizError(status int, code, format string, args ...any) *BizError {
	return &BizError{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}

// BadRequest returns 400.
func BadRequest(format string, args ...any) *BizError {
	return newBizError(http.StatusBadRequest, "BAD_REQUEST", format, args...)
}

// Unauthorized returns 401.
func Unauthorized(format string, args ...any) *BizError {
	return newBizError(http.StatusUnauthorized, "UNAUTHORIZED", format, args...)
}

// Forbidden returns 403.
func Forbidden(format string, args ...any) *BizError {
	return newBizError(http.StatusForbidden, "FORBIDDEN", format, args...)
}

// NotFound returns 404.
func NotFound(format string, args ...any) *BizError {
	return newBizError(http.StatusNotFound, "NOT_FOUND", format, args...)
}

// Conflict returns 409.
func Conflict(format string, args ...any) *BizError {
	return newBizError(http.StatusConflict, "CONFLICT", format, args...)
}

// Internal returns 500.
func Internal(format string, args ...any) *BizError {
	return newBizError(http.StatusInternalServerError, "INTERNAL", format, args...)
}

// Unavailable returns 503.
func Unavailable(format string, args ...any) *BizError {
	return newBizError(http.StatusServiceUnavailable, "UNAVAILABLE", format, args...)
}

// AsError extracts BizError from err.
func AsError(err error) (*BizError, bool) {
	var e *BizError
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// Also expose Err* aliases for compatibility.
func ErrBadRequest(format string, args ...any) *BizError   { return BadRequest(format, args...) }
func ErrUnauthorized(format string, args ...any) *BizError { return Unauthorized(format, args...) }
func ErrForbidden(format string, args ...any) *BizError    { return Forbidden(format, args...) }
func ErrNotFound(format string, args ...any) *BizError     { return NotFound(format, args...) }
func ErrConflict(format string, args ...any) *BizError     { return Conflict(format, args...) }
