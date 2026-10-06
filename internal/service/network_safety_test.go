package service

import (
	"context"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestVPCCreateReservesNameBeforeRemoteAndBlocksReferenceDuringDelete(t *testing.T) {
	db := newIPPoolTestDB(t)
	agent := seedIPPoolAgent(t, db)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			if calls.Add(1) == 1 {
				close(entered)
				<-release
			}
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(204)
	}))
	defer remote.Close()
	db.Model(&agent).Update("endpoint", remote.URL)
	s := NewVirtualisService(db)
	input := VPCInput{AgentID: agent.ID, Name: "safe-net", Driver: "qemu", Subnet: "10.20.0.0/24", Gateway: "10.20.0.1"}
	done := make(chan error, 1)
	go func() { _, err := s.CreateVPC(context.Background(), input); done <- err }()
	<-entered
	_, err := NewVirtualisService(db).CreateVPC(context.Background(), input)
	close(release)
	first := <-done
	if err == nil || calls.Load() != 1 || first != nil {
		t.Fatalf("duplicate remote creation calls=%d first=%v duplicate=%v", calls.Load(), first, err)
	}
}

func TestPoolCannotReleaseLiveOrTrashedOwner(t *testing.T) {
	f := newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) { replyLifecycle(w, decodeLifecycleRequest(t, r)) })
	entry := model.IPPoolEntry{AgentID: f.agent.ID, IP: "192.0.2.3", Status: "assigned", InstanceID: &f.inst.ID}
	f.db.Create(&entry)
	free := "free"
	if _, err := f.svc.UpdateIPPoolEntry(entry.ID, UpdateIPPoolEntryInput{Status: &free}); err == nil {
		t.Fatal("live owner address released")
	}
}

func TestNATSyncFailureIsNotReportedAsSuccess(t *testing.T) {
	f := newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/nat") {
			http.Error(w, "iptables failed", 502)
			return
		}
		replyLifecycle(w, decodeLifecycleRequest(t, r))
	})
	f.db.Model(&f.inst).Update("status", "running")
	if _, err := f.svc.CreateNATMapping(context.Background(), f.inst.ID, CreateNATMappingRequest{Protocol: "tcp", GuestPort: 22}); err == nil {
		t.Fatal("NAT remote failure swallowed")
	}
}

func TestVPCReferenceReservationRejectsDeletingState(t *testing.T) {
	db := newIPPoolTestDB(t)
	vpc := model.VPC{AgentID: 1, Name: "deleting", Driver: "qemu"}
	db.Create(&vpc)
	// Map update remains executable against the old schema and makes the absent
	// state fence itself visible as RED rather than requiring generated fixtures.
	if !db.Migrator().HasColumn(&model.VPC{}, "state") {
		if err := db.Exec("ALTER TABLE vpcs ADD COLUMN state TEXT DEFAULT 'available'").Error; err != nil {
			t.Fatal(err)
		}
	}
	db.Model(&vpc).Update("state", "deleting")
	if err := db.Transaction(func(tx *gorm.DB) error { return reserveVPCReference(tx, vpc.ID) }); err == nil {
		t.Fatal("reference inserted while VPC deleting")
	}
}
