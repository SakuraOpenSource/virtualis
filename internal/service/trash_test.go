package service

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/SakuraOpenSource/virtualis/internal/model"
)

func TestRecycleStopsAndRetainsOwnershipUntilPurge(t *testing.T) {
	deleted, stopped := 0, 0
	f := newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		inst := decodeLifecycleRequest(t, r)
		if r.Method == http.MethodDelete {
			deleted++
			w.WriteHeader(204)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/power") {
			stopped++
			inst.Status = "stopped"
		} else if stopped == 0 {
			inst.Status = "running"
		}
		replyLifecycle(w, inst)
	})
	vpc := model.VPC{AgentID: f.agent.ID, Name: "private-net", Driver: "qemu"}
	if err := f.db.Create(&vpc).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&f.inst).Update("vpc_id", vpc.ID).Error; err != nil {
		t.Fatal(err)
	}
	pool := model.IPPoolEntry{AgentID: f.agent.ID, IP: "192.0.2.10", Status: "assigned", InstanceID: &f.inst.ID}
	if err := f.db.Create(&pool).Error; err != nil {
		t.Fatal(err)
	}
	nat := model.NATMapping{AgentID: f.agent.ID, InstanceID: f.inst.ID, Protocol: "tcp", HostPort: 22000, GuestPort: 22}
	if err := f.db.Create(&nat).Error; err != nil {
		t.Fatal(err)
	}
	snap := model.Snapshot{AgentID: f.agent.ID, InstanceID: f.inst.ID, Name: "base", Status: "available"}
	if err := f.db.Create(&snap).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.svc.DeleteInstance(context.Background(), f.inst.ID); err != nil {
		t.Fatal(err)
	}
	if deleted != 0 || stopped != 1 {
		t.Fatalf("recycle runtime calls delete=%d stop=%d", deleted, stopped)
	}
	if _, err := f.svc.GetInstance(f.inst.ID); err == nil {
		t.Fatal("trash visible as active")
	}
	items, total, err := f.svc.ListTrash(1, 20, 0)
	if err != nil || total != 1 || len(items) != 1 || items[0].PurgeAfter == nil {
		t.Fatalf("trash=%+v total=%d err=%v", items, total, err)
	}
	if err = f.svc.DeleteVPC(context.Background(), vpc.ID); err == nil {
		t.Fatal("trashed VPC reference ignored")
	}
	var retained model.IPPoolEntry
	f.db.First(&retained, pool.ID)
	if retained.InstanceID == nil {
		t.Fatal("recycle released IP")
	}
	restored, err := f.svc.RestoreTrashedInstance(context.Background(), f.inst.ID)
	if err != nil || restored.Status != "stopped" || restored.TrashedAt != nil {
		t.Fatalf("restore=%+v %v", restored, err)
	}
	if err = f.svc.DeleteInstance(context.Background(), f.inst.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.svc.PurgeInstance(context.Background(), f.inst.ID); err != nil {
		t.Fatal(err)
	}
	f.db.First(&retained, pool.ID)
	if retained.InstanceID != nil || retained.Status != "free" {
		t.Fatalf("purge did not release IP: %+v", retained)
	}
	for _, entity := range []any{&model.Instance{}, &model.Snapshot{}, &model.NATMapping{}} {
		var n int64
		f.db.Model(entity).Count(&n)
		if n != 0 {
			t.Fatalf("purge retained %T", entity)
		}
	}
	if deleted != 1 {
		t.Fatalf("runtime not purged: %d", deleted)
	}
}

func TestRetentionCleanupHasPerEntryFailureAndKeepsFailedRecord(t *testing.T) {
	f := newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		inst := decodeLifecycleRequest(t, r)
		if r.Method == http.MethodDelete && inst.Name == "life-guest" {
			http.Error(w, `{"error":"busy disk"}`, 502)
			return
		}
		replyLifecycle(w, inst)
	})
	now := time.Now().UTC()
	past := now.Add(-time.Hour)
	if err := f.db.Model(&f.inst).Updates(map[string]any{"trashed_at": past, "purge_after": past}).Error; err != nil {
		t.Fatal(err)
	}
	second := f.inst
	second.Base = model.Base{}
	second.Name = "second"
	second.TrashedAt = &past
	second.PurgeAfter = &past
	if err := f.db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	result, err := f.svc.CleanupTrash(context.Background(), now, 100)
	if err != nil || len(result.OK) != 1 || result.OK[0] != second.ID || len(result.Failed) != 1 || result.Failed[0].ID != f.inst.ID {
		t.Fatalf("cleanup=%+v %v", result, err)
	}
	var n int64
	f.db.Model(&model.Instance{}).Where("id = ?", f.inst.ID).Count(&n)
	if n != 1 {
		t.Fatal("failed purge removed record")
	}
}
