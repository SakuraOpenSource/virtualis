package middleware

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/SakuraOpenSource/virtualis/internal/auth"
	"github.com/SakuraOpenSource/virtualis/internal/httpx"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"github.com/SakuraOpenSource/virtualis/internal/runtime"
)

// RequireInstalled blocks requests when app is not installed yet.
func RequireInstalled(rt *runtime.Runtime) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !rt.Installed() {
			httpx.Fail(c, http.StatusServiceUnavailable, httpx.CodeNotInstalled, "not installed")
			return
		}
		c.Next()
	}
}

// RequireAuth checks token cookie and loads user into context.
func RequireAuth(rt *runtime.Runtime) gin.HandlerFunc {
	return func(c *gin.Context) {
		tok, err := c.Cookie(auth.CookieToken)
		if err != nil || tok == "" {
			httpx.Unauthorized(c, "login required")
			return
		}
		claims, err := auth.ParseToken(rt.JWTSecret(), tok)
		if err != nil {
			httpx.Unauthorized(c, "session expired, please login again")
			return
		}
		var u model.User
		if err := rt.DB().First(&u, claims.UserID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				httpx.Unauthorized(c, "account not found")
				return
			}
			httpx.Internal(c, "")
			return
		}
		if u.Status != model.StatusActive {
			httpx.Forbidden(c, "account disabled")
			return
		}
		httpx.SetUser(c, &u)
		c.Next()
	}
}

// RequireAdmin ensures current user is admin. Must be after RequireAuth.
func RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		u := httpx.CurrentUser(c)
		if u == nil {
			httpx.Unauthorized(c, "login required")
			return
		}
		if !u.IsAdmin() {
			httpx.Forbidden(c, "admin required")
			return
		}
		c.Next()
	}
}

// CSRF protects mutating requests with double-submit cookie.
func CSRF() gin.HandlerFunc {
	return func(c *gin.Context) {
		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			if ck, err := c.Cookie(auth.CookieCSRF); err != nil || ck == "" {
				setCSRFCookie(c)
			}
			c.Next()
			return
		}
		cookie, err := c.Cookie(auth.CookieCSRF)
		if err != nil || !auth.SecureCompare(cookie, c.GetHeader(auth.HeaderCSRF)) {
			httpx.Forbidden(c, "csrf check failed")
			return
		}
		c.Next()
	}
}

func setCSRFCookie(c *gin.Context) {
	tok, err := auth.GenerateCSRFToken()
	if err != nil {
		return
	}
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(auth.CookieCSRF, tok, int(auth.TokenTTL.Seconds()), "/", "", isSecure(c), false)
}

func isSecure(c *gin.Context) bool {
	if c.Request.TLS != nil {
		return true
	}
	return strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https")
}
