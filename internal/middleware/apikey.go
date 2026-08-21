package middleware

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/SakuraOpenSource/virtualis/internal/httpx"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"github.com/SakuraOpenSource/virtualis/internal/runtime"
)

const ctxAPIKeyScopes = "virtualis_api_scopes"
const headerAPIKeyAlt = "X-Virtualis-Api-Key"

// RequireAPIKey authenticates via API key header and injects user.
func RequireAPIKey(rt *runtime.Runtime) gin.HandlerFunc {
	return func(c *gin.Context) {
		secret := extractKey(c)
		if secret == "" {
			httpx.Unauthorized(c, "api key required")
			return
		}
		hash := hashKey(secret)
		var k model.APIKey
		if err := rt.DB().Where("key_hash = ?", hash).First(&k).Error; err != nil {
			httpx.Unauthorized(c, "invalid api key")
			return
		}
		if !k.Usable(time.Now()) {
			httpx.Unauthorized(c, "api key revoked or expired")
			return
		}
		var u model.User
		if err := rt.DB().First(&u, k.UserID).Error; err != nil {
			httpx.Unauthorized(c, "api key invalid")
			return
		}
		if u.Status != model.StatusActive {
			httpx.Forbidden(c, "account disabled")
			return
		}
		// Best-effort update last used.
		now := time.Now()
		_ = rt.DB().Model(&k).Update("last_used_at", now).Error

		httpx.SetUser(c, &u)
		c.Set(ctxAPIKeyScopes, k.Scopes)
		c.Next()
	}
}

// RequireScope ensures the api key has the required scope.
func RequireScope(scope string) gin.HandlerFunc {
	return func(c *gin.Context) {
		v, ok := c.Get(ctxAPIKeyScopes)
		if !ok {
			httpx.Unauthorized(c, "api key required")
			return
		}
		scopes, _ := v.(model.ScopeList)
		if !scopes.Has(scope) {
			httpx.Forbidden(c, "missing scope "+scope)
			return
		}
		c.Next()
	}
}

func extractKey(c *gin.Context) string {
	if h := c.GetHeader("Authorization"); h != "" {
		if val, ok := bearerToken(h); ok {
			return strings.TrimSpace(val)
		}
		return ""
	}
	return strings.TrimSpace(c.GetHeader(headerAPIKeyAlt))
}

func bearerToken(h string) (string, bool) {
	const prefix = "Bearer "
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	return h[len(prefix):], true
}

func hashKey(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
