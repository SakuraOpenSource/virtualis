package service

import (
	"context"
	"encoding/json"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestDedicatedAutoExhaustionAndFailedCreateKeepReservationUntilVerifiedPurge(t *testing.T) {
	db := newIPPoolTestDB(t)
	agent := seedIPPoolAgent(t, db)
	svc := NewVirtualisService(db)
	var creates atomic.Int32
	var cleanupOK atomic.Bool
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/drivers":
			json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{{"name": "qemu", "available": true}}})
		case r.URL.Path == "/api/host/network":
			json.NewEncoder(w).Encode(map[string]any{"network": map[string]any{"ipv4_count": 1, "interfaces": []map[string]any{{"name": "eth0", "kind": "physical", "state": "up", "ipv4": []string{"192.0.2.1/24"}}}}})
		case r.Method == "POST" && r.URL.Path == "/api/instances":
			creates.Add(1)
			http.Error(w, "fixture uncertain create failure", 502)
		case r.Method == "DELETE":
			if cleanupOK.Load() {
				w.WriteHeader(204)
			} else {
				http.Error(w, "fixture uncertain delete failure", 502)
			}
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer remote.Close()
	db.Model(&agent).Update("endpoint", remote.URL)
	svc.SaveIPPoolDefaults(agent.ID, IPPoolInput{Gateway: "192.0.2.1", Prefix: 24, Interface: "eth0"})
	svc.AddIPPoolEntries(agent.ID, AddIPPoolEntriesInput{IPs: []string{"192.0.2.10"}})
	no := false
	req := CreateInstanceRequest{Name: "uncertain-create", Driver: "qemu", AgentID: &agent.ID, Network: model.NetworkConfig{Mode: "dedicated"}, AutoPassword: &no}
	inst, err := svc.CreateInstance(context.Background(), req)
	if err == nil || inst == nil || inst.ID == 0 {
		t.Fatalf("want retained failed instance: %v", err)
	}
	var entry model.IPPoolEntry
	db.Where("instance_id = ?", inst.ID).First(&entry)
	if entry.Status != "assigned" {
		t.Fatal("uncertain reservation recycled")
	}
	req.Name = "exhausted"
	if _, err = svc.CreateInstance(context.Background(), req); err == nil {
		t.Fatal("exhausted allocation succeeded")
	}
	if e, ok := AsError(err); !ok || e.Status != 409 {
		t.Fatalf("exhaustion must be conflict: %v", err)
	}
	if creates.Load() != 1 {
		t.Fatal("exhaustion reached remote provisioning")
	}
	var count int64
	db.Model(&model.Instance{}).Where("name = ?", req.Name).Count(&count)
	if count != 0 {
		t.Fatal("exhausted instance persisted")
	}
	if err = svc.PurgeInstance(context.Background(), inst.ID); err == nil {
		t.Fatal("uncertain cleanup reported success")
	}
	db.First(&entry, entry.ID)
	if entry.InstanceID == nil {
		t.Fatal("IP freed before remote delete proved")
	}
	cleanupOK.Store(true)
	if err = svc.PurgeInstance(context.Background(), inst.ID); err != nil {
		t.Fatal(err)
	}
	var released model.IPPoolEntry
	db.First(&released, entry.ID)
	if released.InstanceID != nil || released.Status != "free" {
		t.Fatal("verified hard delete did not release")
	}
}

func TestDedicatedReservationRejectsManualUseAndCorruptFreeOwnership(t *testing.T) {
	db := newIPPoolTestDB(t)
	agent := seedIPPoolAgent(t, db)
	owner := uint(42)
	entry := model.IPPoolEntry{AgentID: agent.ID, IP: "192.0.2.10", Status: "assigned", InstanceID: &owner}
	db.Create(&entry)
	svc := NewVirtualisService(db)
	taken, err := svc.dedicatedIPTaken(agent.ID, "192.0.2.10/24", 0)
	if err != nil || !taken {
		t.Fatalf("migration/uncertain pool reservation ignored: %v %v", taken, err)
	}
	db.Model(&entry).Update("status", "free")
	if err = svc.assignPoolEntry(entry.ID, 43); err == nil {
		t.Fatal("CAS stole corrupt free-but-owned reservation")
	}
}
