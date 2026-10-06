package server

import (
	"github.com/SakuraOpenSource/virtualis/internal/handler"
	"github.com/SakuraOpenSource/virtualis/internal/middleware"
	"github.com/gin-gonic/gin"
)

func registerRecoveryRoutes(group *gin.RouterGroup, h *handler.Handler, session bool) {
	group.GET("/instances/:id/snapshots", h.Snapshots)
	group.GET("/instances/:id/backups", h.Backups)
	group.GET("/instances/:id/backups/:bid/download", h.DownloadBackup)
	group.GET("/trash", h.Trash)
	group.POST("/instances/batch", h.BatchInstances)
	writes := group
	if session {
		writes = group.Group("", middleware.RequireAdmin())
	}
	writes.PATCH("/instances/:id/spec", h.ResizeInstance)
	writes.POST("/instances/:id/migrate", h.MigrateInstance)
	writes.GET("/instances/:id/migrations", h.Migrations)
	writes.POST("/trash/:id/restore", h.RestoreTrash)
	writes.DELETE("/trash/:id", h.PurgeInstance)
	writes.DELETE("/instances/:id/purge", h.PurgeInstance)
	writes.POST("/instances/:id/snapshots", h.CreateSnapshot)
	writes.POST("/instances/:id/snapshots/:sid/restore", h.RestoreSnapshot)
	writes.DELETE("/instances/:id/snapshots/:sid", h.DeleteSnapshot)
	writes.POST("/instances/:id/backups", h.CreateBackup)
	writes.POST("/instances/:id/backups/:bid/restore", h.RestoreBackup)
	writes.DELETE("/instances/:id/backups/:bid", h.DeleteBackup)
}
