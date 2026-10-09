package service

import (
	"context"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestVPCCompensationSurvivesRequestCancellation(t *testing.T) {
	db := newIPPoolTestDB(t)
	agent := seedIPPoolAgent(t, db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deletes := 0
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			deletes++
		}
		w.WriteHeader(200)
	}))
	defer remote.Close()
	db.Callback().Update().Before("gorm:update").Register("cancel_finalize", func(tx *gorm.DB) {
		if tx.Statement.Table == "vpcs" {
			cancel()
			tx.AddError(context.Canceled)
		}
	})
	if err := db.Model(&agent).Update("endpoint", remote.URL).Error; err != nil {
		t.Fatal(err)
	}
	_, err := NewVirtualisService(db).CreateVPC(ctx, VPCInput{AgentID: agent.ID, Name: "cancel-net", Driver: "qemu", Subnet: "10.0.0.0/24", Gateway: "10.0.0.1"})
	if err == nil {
		t.Fatal("canceled finalization returned success")
	}
	var rows []model.VPC
	if e := db.Find(&rows).Error; e != nil {
		t.Fatal(e)
	}
	if deletes != 1 || len(rows) != 0 {
		t.Fatalf("independent compensation failed: deletes=%d rows=%+v", deletes, rows)
	}
}

func TestRetentionZeroDisablesScheduledPurge(t *testing.T) {
	f := newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("disabled scheduler touched Agent")
		w.WriteHeader(204)
	})
	now := time.Now().UTC().Add(-time.Hour)
	if err := f.db.Model(&f.inst).Updates(map[string]any{"trashed_at": now, "purge_after": now}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SaveRetention(RetentionSettings{RetentionDays: 0}); err != nil {
		t.Fatal(err)
	}
	result, err := f.svc.CleanupTrash(context.Background(), time.Now().UTC(), 100)
	if err != nil || len(result.OK) != 0 {
		t.Fatalf("disabled retention purged: %+v %v", result, err)
	}
	var inst model.Instance
	if err = f.db.First(&inst, f.inst.ID).Error; err != nil {
		t.Fatal("disabled retention deleted instance")
	}
}
