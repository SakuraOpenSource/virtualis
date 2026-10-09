package server

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/SakuraOpenSource/virtualis/internal/handler"
	"github.com/SakuraOpenSource/virtualis/internal/middleware"
	"github.com/SakuraOpenSource/virtualis/internal/runtime"
	"github.com/SakuraOpenSource/virtualis/internal/web"
)

const (
	bodyLimit       = 1 << 20 // 1 MiB for JSON bodies
	multipartMemory = 8 << 20 // 8 MiB for multipart
	imageBodyLimit  = int64(64 << 30)
)

// New builds the gin engine with all routes.
func New(rt *runtime.Runtime, debug bool) (*gin.Engine, func()) {
	if !debug {
		gin.SetMode(gin.ReleaseMode)
	}
	eng := gin.New()
	// 只信任回环代理（生产 Nginx 与后端同机）：gin 默认信任一切代理头，
	// 远端可伪造 X-Forwarded-For 篡改被控注册时记录的 IP。直连（本地开发）
	// 没有代理头，ClientIP 就是直连地址。与 Levis 主程序同一口径。
	if err := eng.SetTrustedProxies([]string{"127.0.0.1", "::1"}); err != nil {
		panic(fmt.Sprintf("设置可信代理失败: %v", err))
	}
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
	authGroup.POST("/logout", middleware.RequireAuth(rt, h.Revoker()), h.Logout)

	// Authenticated routes.
	authed := secured.Group("", middleware.RequireAuth(rt, h.Revoker()), middleware.InstanceOwnership(rt))

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
	authed.GET("/vpcs", h.VPCs)
	authed.POST("/vpcs", middleware.RequireAdmin(), h.CreateVPC)
	authed.DELETE("/vpcs/:id", middleware.RequireAdmin(), h.DeleteVPC)
	authed.GET("/instances/:id/firewall", h.Firewall)
	authed.POST("/instances/:id/firewall", middleware.RequireAdmin(), h.CreateFirewall)
	authed.PATCH("/firewall/:id", middleware.RequireAdmin(), h.UpdateFirewall)
	authed.DELETE("/firewall/:id", middleware.RequireAdmin(), h.DeleteFirewall)

	// IP pool: free addresses feed the instance-create dialog (agent-scoped);
	// management endpoints live under /admin below.
	authed.GET("/ip-pools/:agentID/free", h.FreeIPPoolEntries)

	// Instances
	authed.GET("/instances", h.Instances)
	authed.POST("/instances", middleware.RequireAdmin(), h.CreateInstance)
	authed.GET("/instances/:id", h.Instance)
	authed.GET("/instances/:id/security-groups", h.InstanceSecurityGroups)
	authed.PUT("/instances/:id/security-groups", middleware.RequireAdmin(), h.SetInstanceSecurityGroups)
	authed.DELETE("/instances/:id", h.DeleteInstance)
	authed.POST("/instances/:id/power", h.InstancePower)
	authed.GET("/instances/:id/status", h.InstanceStatus)
	authed.GET("/instances/:id/metrics", h.InstanceMetrics)
	authed.GET("/instances/:id/network", h.InstanceNetwork)
	authed.POST("/instances/:id/network/configure", h.InstanceConfigureNetwork)
	authed.GET("/instances/:id/logs", h.InstanceOperationLogs)
	registerRecoveryRoutes(authed, h, true)
	authed.POST("/instances/:id/nat", h.InstanceNATCreate)
	authed.DELETE("/instances/:id/nat/:mid", h.InstanceNATDelete)
	authed.POST("/instances/:id/password", h.InstancePasswordSet)
	authed.GET("/instances/:id/vnc", h.InstanceVNC)
	authed.GET("/instances/:id/vnc/ws", h.InstanceVNCWebSocket)
	// 短票通道：浏览器带一次性 ticket 建连，不经过会话 Cookie。
	// 路径与会话版错开，避免 Gin 同 method+path 重复注册 panic。
	secured.GET("/instances/:id/vnc/ws-ticket", h.InstanceVNCWebSocketByTicket)

	// Images: read requires auth, write requires admin
	authed.GET("/images", h.Images)
	authed.GET("/images/:id", h.Image)
	// Write operations are additionally gated by admin.
	adminImages := authed.Group("/images", middleware.RequireAdmin())
	adminImages.POST("", h.CreateImage)
	adminImages.POST("/upload", h.UploadImage)
	adminImages.POST("/download", h.StartImageDownload)
	adminImages.GET("/presets", h.ImagePresets)
	adminImages.GET("/:id/download", h.DownloadImage)
	adminImages.DELETE("/:id", h.DeleteImage)

	// Admin-only settings & agents
	admin := authed.Group("/admin", middleware.RequireAdmin())
	admin.GET("/security-groups", h.SecurityGroups)
	admin.POST("/security-groups", h.CreateSecurityGroup)
	admin.GET("/security-groups/:id", h.SecurityGroup)
	admin.PATCH("/security-groups/:id", h.UpdateSecurityGroup)
	admin.DELETE("/security-groups/:id", h.DeleteSecurityGroup)
	admin.PUT("/security-groups/:id/rules", h.ReplaceSecurityGroupRules)
	admin.GET("/settings", h.Settings)
	admin.PUT("/settings", h.UpdateSettings)
	admin.GET("/settings/virtualis", h.VirtualisSettings)
	admin.PUT("/settings/virtualis", h.UpdateVirtualisSettings)
	admin.GET("/settings/retention", h.Retention)
	admin.PUT("/settings/retention", h.SaveRetention)
	admin.GET("/settings/captcha", h.CaptchaSettings)
	admin.PUT("/settings/captcha", h.UpdateCaptchaSettings)
	admin.GET("/agents", h.Agents)
	admin.GET("/agents/:id/network", h.AgentHostNetwork)
	admin.POST("/agents", h.CreateAgent)
	admin.POST("/agents/:id/rotate-token", h.RotateAgentToken)
	admin.DELETE("/agents/:id", h.DeleteAgent)

	// Dedicated-IP pools: defaults + entries per agent.
	admin.GET("/ip-pools", h.IPPools)
	admin.GET("/ip-pools/:agentID", h.IPPool)
	admin.PUT("/ip-pools/:agentID", h.SaveIPPool)
	admin.POST("/ip-pools/:agentID/entries", h.AddIPPoolEntries)
	admin.PATCH("/ip-pool-entries/:id", h.UpdateIPPoolEntry)
	admin.DELETE("/ip-pool-entries/:id", h.DeleteIPPoolEntry)

	// Agent self-registration (no CSRF, token-based) - must be outside CSRF group
	agentAPI := eng.Group("/api/agent", middleware.RequireInstalled(rt))
	agentAPI.POST("/register", h.AgentRegister)
	agentAPI.GET("/install.sh", h.AgentInstallScript)
	agentAPI.GET("/binary", h.AgentBinary)
	agentAPI.HEAD("/binary", h.AgentBinary)

	// Machine-to-machine open API: site API key auth instead of session
	// cookies, so it also lives outside the CSRF group.
	v1 := eng.Group("/api/v1", middleware.RequireInstalled(rt), middleware.RequireAPIKey(rt), middleware.APIRequestScope(), middleware.InstanceOwnership(rt))
	providerGroups := v1.Group("/security-groups", middleware.RequireAdmin())
	providerGroups.GET("", h.SecurityGroups)
	providerGroups.POST("", h.CreateSecurityGroup)
	providerGroups.GET("/:id", h.SecurityGroup)
	providerGroups.PATCH("/:id", h.UpdateSecurityGroup)
	providerGroups.DELETE("/:id", h.DeleteSecurityGroup)
	providerGroups.PUT("/:id/rules", h.ReplaceSecurityGroupRules)
	v1.GET("/instances/:id/security-groups", h.InstanceSecurityGroups)
	v1.PUT("/instances/:id/security-groups", middleware.RequireAdmin(), h.SetInstanceSecurityGroups)
	v1.GET("/images", h.V1Images)
	v1.POST("/instances", h.V1CreateInstance)
	v1.GET("/agents", h.V1Agents)
	v1.GET("/vpcs", h.VPCs)
	v1.GET("/ip-pools/:agentID/free", h.FreeIPPoolEntries)
	v1.GET("/instances/:id/firewall", h.Firewall)
	v1.POST("/instances/:id/firewall", h.CreateFirewall)
	v1.PATCH("/firewall/:id", h.UpdateFirewall)
	v1.DELETE("/firewall/:id", h.DeleteFirewall)
	v1.GET("/instances", h.Instances)
	v1.GET("/instances/:id", h.Instance)
	v1.GET("/instances/:id/status", h.InstanceStatus)
	v1.GET("/instances/:id/metrics", h.InstanceMetrics)
	v1.GET("/instances/:id/network", h.InstanceNetwork)
	v1.GET("/instances/:id/access", h.V1InstanceAccess)
	v1.DELETE("/instances/:id", h.DeleteInstance)
	v1.POST("/instances/:id/power", h.InstancePower)
	registerRecoveryRoutes(v1, h, false)
	v1.GET("/instances/:id/logs", h.InstanceOperationLogs)
	// NAT 端口映射：会话版在 /api/instances/:id/nat，v1 路径树不同不会撞注册。
	v1.GET("/instances/:id/nat", h.V1ListNATMappings)
	v1.POST("/instances/:id/nat", h.V1CreateNATMapping)
	v1.DELETE("/instances/:id/nat/:mid", h.V1DeleteNATMapping)
	// VNC 短票签发：机器对接方凭站点 Key 领取，转交最终用户浏览器建连。
	v1.POST("/instances/:id/vnc-ticket", h.V1CreateVNCTicket)

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
		limit := int64(bodyLimit)
		if strings.HasPrefix(strings.ToLower(c.GetHeader("Content-Type")), "multipart/form-data") {
			limit = imageBodyLimit
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
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
