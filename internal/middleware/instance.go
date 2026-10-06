package middleware

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/SakuraOpenSource/virtualis/internal/httpx"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"github.com/SakuraOpenSource/virtualis/internal/runtime"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// APIRequestScope applies scopes to every v1 path, including shared session
// handlers. Being an administrator never bypasses the key's scope.
func APIRequestScope() gin.HandlerFunc {
	return func(c *gin.Context) {
		scope := model.ScopeInstanceRead
		write := c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead
		if write {
			scope = model.ScopeInstanceWrite
		}
		if strings.HasPrefix(c.FullPath(), "/api/v1/images") {
			scope = model.ScopeImageRead
			if write {
				scope = model.ScopeImageWrite
			}
		}
		RequireScope(scope)(c)
	}
}

// InstanceOwnership also covers firewall rows and trash. Looking up the exact
// resource (not trusting a request owner_id) closes direct-ID access paths.
func InstanceOwnership(rt *runtime.Runtime) gin.HandlerFunc {
	return func(c *gin.Context) {
		user := httpx.CurrentUser(c)
		if user == nil {
			httpx.Unauthorized(c, "login required")
			return
		}
		path := c.FullPath()
		if !strings.Contains(path, "/instances/:id") && !strings.Contains(path, "/trash/:id") && !strings.Contains(path, "/firewall/:id") {
			c.Next()
			return
		}
		id, err := strconv.ParseUint(c.Param("id"), 10, 64)
		if err != nil || id == 0 {
			httpx.BadRequest(c, "invalid id")
			return
		}
		if strings.Contains(path, "/firewall/:id") {
			var rule model.FirewallRule
			if err = rt.DB().First(&rule, uint(id)).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					httpx.NotFound(c, "rule not found")
				} else {
					httpx.Internal(c, "")
				}
				return
			}
			id = uint64(rule.InstanceID)
		}
		var inst model.Instance
		if err = rt.DB().First(&inst, uint(id)).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				httpx.NotFound(c, "instance not found")
			} else {
				httpx.Internal(c, "")
			}
			return
		}
		if !user.IsAdmin() && (inst.OwnerID == nil || *inst.OwnerID != user.ID) {
			httpx.Forbidden(c, "instance ownership required")
			return
		}
		c.Next()
	}
}
