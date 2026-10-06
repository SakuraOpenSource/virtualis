package handler

import (
	"context"
	"github.com/SakuraOpenSource/virtualis/internal/config"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"github.com/SakuraOpenSource/virtualis/internal/runtime"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"path/filepath"
	"testing"
	"time"
)

func TestSingletonBindsAfterInstallAndSchedulerPurgesExpiredTrash(t *testing.T) {
	dir := t.TempDir()
	rt := runtime.New(dir)
	h := New(rt)
	db, err := gorm.Open(sqlite.Open(filepath.Join(dir, "handler.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	defer h.Close()
	if err = db.AutoMigrate(model.AllModels()...); err != nil {
		t.Fatal(err)
	}
	rt.Activate(&config.Config{}, db)
	now := time.Now().Add(-time.Hour)
	inst := model.Instance{Name: "expired", TrashedAt: &now, PurgeAfter: &now}
	if err = db.Create(&inst).Error; err != nil {
		t.Fatal(err)
	}
	if h.virtualis() != h.virtualis() {
		t.Fatal("not singleton")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = h.cleanupTrash(ctx); err != nil {
		t.Fatal(err)
	}
	var n int64
	db.Model(&model.Instance{}).Count(&n)
	if n != 0 {
		t.Fatal("scheduler retained expired trash")
	}
}
