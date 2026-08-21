package server

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/SakuraOpenSource/virtualis/internal/handler"
	"github.com/SakuraOpenSource/virtualis/internal/middleware"
	"github.com/SakuraOpenSource/virtualis/internal/runtime"
	"github.com/SakuraOpenSource/virtualis/internal/web"
)

const (
	bodyLimit       = 1 << 20       // 1 MiB for JSON bodies
	multipartMemory = 8 << 20       // 8 MiB for multipart
)

// New builds the gin engine with all routes.
func New(rt *runtime.Runtime, debug bool) (*gin.Engine, func()) {
	if !debug {
		gin.SetMode(gin.ReleaseMode)
	}
	eng := gin.New()
	eng.Use(gin.Logger(), gin.Recovery(), limitBody(), securityHeaders())
	eng.MaxMultipartMemory = multipartMemory
	eng.RedirectTrailingSlash = false

	h := handler.New(rt)

	// All /api routes share CSRF double-submit protection.
	api := eng.Group("/api", middleware.CSRF())

	// Bootstrap and install are accessible before installation.
	api.GET("/bootstrap", h.Bootstrap)
	install := api.Group("/install")
	install.POST("/test-db", h.TestDatabase)
	install.POST("", h.Install)

	// Everything else requires installation to be complete.
	secured := api.Group("", middleware.RequireInstalled(rt))
	secured.GET("/captcha", h.Captcha)

	authGroup := secured.Group("/auth")
	authGroup.POST("/login", h.Login)
	// logout can be called with or without fresh auth; protect with auth when possible
	authGroup.POST("/logout", middleware.RequireAuth(rt), h.Logout)

	// Authenticated routes.
	authed := secured.Group("", middleware.RequireAuth(rt))

	authed.GET("/me", h.Me)
	authed.PATCH("/me/email", h.UpdateEmail)
	authed.POST("/me/password", h.UpdatePassword)

	// API keys (no KYC in Virtualis)
	apikeys := authed.Group("/api-keys")
	apikeys.GET("", h.APIKeys)
	apikeys.POST("", h.CreateAPIKey)
	apikeys.DELETE("/:id", h.RevokeAPIKey)

	// Drivers
	authed.GET("/drivers", h.Drivers)

	// Instances
	authed.GET("/instances", h.Instances)
	authed.POST("/instances", h.CreateInstance)
	authed.GET("/instances/:id", h.Instance)
	authed.DELETE("/instances/:id", h.DeleteInstance)
	authed.POST("/instances/:id/power", h.InstancePower)
	authed.GET("/instances/:id/status", h.InstanceStatus)

	// Images: read requires auth, write requires admin
	authed.GET("/images", h.Images)
	authed.GET("/images/:id", h.Image)
	// Write operations are additionally gated by admin.
	adminImages := authed.Group("/images", middleware.RequireAdmin())
	adminImages.POST("", h.CreateImage)
	adminImages.DELETE("/:id", h.DeleteImage)

	// Admin-only settings & agents
	admin := authed.Group("/admin", middleware.RequireAdmin())
	admin.GET("/settings", h.Settings)
	admin.PUT("/settings", h.UpdateSettings)
	admin.GET("/settings/virtualis", h.VirtualisSettings)
	admin.PUT("/settings/virtualis", h.UpdateVirtualisSettings)
	admin.GET("/settings/captcha", h.CaptchaSettings)
	admin.PUT("/settings/captcha", h.UpdateCaptchaSettings)
	admin.GET("/agents", h.Agents)
	admin.POST("/agents", h.CreateAgent)
	admin.DELETE("/agents/:id", h.DeleteAgent)

	// Agent self-registration (no CSRF, token-based) - must be outside CSRF group
	agentAPI := eng.Group("/api/agent", middleware.RequireInstalled(rt))
	agentAPI.POST("/register", h.AgentRegister)
	agentAPI.GET("/install.sh", h.AgentInstallScript)

	// SPA fallback + API 404
	frontend := gin.WrapF(web.Handler())
	eng.NoRoute(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api/") || c.Request.URL.Path == "/api" {
			handler.NotFound(c, "not found")
			return
		}
		frontend(c)
	})

	return eng, func() { h.Close() }
}

func limitBody() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, bodyLimit)
		c.Next()
	}
}

func securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "strict-origin-when-cross-origin")
		c.Header("X-XSS-Protection", "0")
		c.Next()
	}
}
