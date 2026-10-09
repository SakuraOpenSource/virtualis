package middleware

import (
	"errors"
	"net/http"
	"strings"
	"time"

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
//
// The database is consulted on every request rather than trusting the JWT's
// embedded role, so disabling an account takes effect immediately. Three
// levels of session invalidation apply:
//  1. jti present in the (durable, shared) revocation list — user logged
//     out; every replica and restart enforces it (see
//     auth.PersistentRevocationList);
//  2. token issued before the user's most recent password change — rotating
//     the password kicks every older device, which is the only self-service
//     action available once a credential might be stolen, so it must be
//     immediate;
//  3. token session_version differs from the account's current counter —
//     the authoritative equality check closing the same-second window of
//     the timestamp comparison above.
//
// revoker may be nil (logout revocation disabled); the password and version
// comparisons do not depend on it.
func RequireAuth(rt *runtime.Runtime, revoker *auth.PersistentRevocationList) gin.HandlerFunc {
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
		if revoker != nil && revoker.IsRevoked(claims.ID) {
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
		// iat has only second precision (jwt v5 serializes whole seconds), so
		// the change timestamp is truncated before comparing; otherwise a
		// token re-issued immediately after a password change would be
		// rejected by its own issuance moment. The session_version check
		// below is the authoritative guard — it closes the same-second
		// window this comparison cannot.
		if u.PasswordChangedAt != nil && claims.IssuedAt != nil &&
			claims.IssuedAt.Time.Before(u.PasswordChangedAt.Truncate(time.Second)) {
			httpx.Unauthorized(c, "password changed, please login again")
			return
		}
		// Exact session-version equality: the counter increments atomically
		// with every password change, so any token issued before the change
		// (same second or not, including pre-upgrade tokens without a
		// sess_ver claim once the account has rotated) is refused here.
		if claims.SessionVersion != u.SessionVersion {
			httpx.Unauthorized(c, "password changed, please login again")
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
