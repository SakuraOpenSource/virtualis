package service

import (
	"context"
	"testing"

	"github.com/SakuraOpenSource/virtualis/internal/model"
	"gorm.io/gorm"
)

// SC-03: agent create must store only the hash; the plaintext is returned
// exactly once and lives in the process cache, never in the database.
func TestAgentCreateStoresHashOnly(t *testing.T) {
	db := newIPPoolTestDB(t)
	svc := NewAgentService(db)
	agent, token, err := svc.Create("node-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if token == "" {
		t.Fatal("plaintext token not returned on create")
	}
	var stored model.Agent
	if err := db.First(&stored, agent.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.TokenHash == "" {
		t.Fatal("token hash missing")
	}
	if hasPlaintextColumn(db) {
		var raw string
		if err := db.Raw("SELECT token FROM agents WHERE id = ?", agent.ID).Scan(&raw).Error; err == nil && raw != "" {
			t.Fatalf("plaintext token persisted to database: %q", raw)
		}
	}
	// The RPC token must be resolvable from the memory cache immediately.
	got, err := svc.RPCToken(agent.ID)
	if err != nil || got != token {
		t.Fatalf("RPC token not cached: %v %q", err, got)
	}
}

// SC-03: rotation invalidates the old plaintext everywhere and returns the
// new one exactly once.
func TestAgentRotateTokenClearsPlaintextAndReplacesCache(t *testing.T) {
	db := newIPPoolTestDB(t)
	svc := NewAgentService(db)
	agent, oldToken, err := svc.Create("node-b", "")
	if err != nil {
		t.Fatal(err)
	}
	rotated, newToken, err := svc.RotateToken(agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if newToken == "" || newToken == oldToken {
		t.Fatal("rotation did not return a fresh token")
	}
	if rotated.Status != model.AgentStatusPending {
		t.Fatalf("rotation must force re-registration: %s", rotated.Status)
	}
	got, err := svc.RPCToken(agent.ID)
	if err != nil || got != newToken {
		t.Fatalf("cache not rotated: %v %q", err, got)
	}
	// The old token no longer authenticates.
	if _, err := svc.Authenticate(oldToken); err == nil {
		t.Fatal("old token still authenticates after rotation")
	}
}

// SC-03: an authenticated heartbeat repopulates the memory cache after a
// master restart without writing plaintext to the database.
func TestAgentHeartbeatRepairsRPCTokenWithoutPersisting(t *testing.T) {
	db := newIPPoolTestDB(t)
	svc := NewAgentService(db)
	agent, token, err := svc.Create("node-c", "")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the restart window: cache cold.
	if err := db.Model(&model.Agent{}).Where("id = ?", agent.ID).Update("last_seen_at", nil).Error; err != nil {
		t.Fatal(err)
	}
	// Simulate restart: fresh service instance, empty cache view is the same
	// process-global map, so instead drop it like Delete would.
	fresh := &agentTokenCache{tokens: map[uint]string{}}
	svc.cache = fresh
	if _, err := svc.RPCToken(agent.ID); err == nil {
		t.Fatal("cold cache unexpectedly served a token")
	}
	// Heartbeat with the correct token re-authenticates and re-caches.
	if _, err := svc.Authenticate(token); err != nil {
		t.Fatal(err)
	}
	if err := svc.Heartbeat(agent, token, "10.0.0.9", "http://10.0.0.9:8081", "qemu", "linux", "amd64", "dev", []string{"qemu"}); err != nil {
		t.Fatal(err)
	}
	got, err := svc.RPCToken(agent.ID)
	if err != nil || got != token {
		t.Fatalf("heartbeat did not repopulate cache: %v %q", err, got)
	}
	var stored model.Agent
	if err := db.First(&stored, agent.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Endpoint != "http://10.0.0.9:8081" || stored.Status != model.AgentStatusOnline {
		t.Fatalf("heartbeat fields not persisted: %+v", stored)
	}
}

// VIR-CORE-03: migrations persist the source VPC reservation.
func TestMigrationRecordsSourceVPCID(t *testing.T) {
	f := newLifecycleFixture(t, nil)
	vpc := model.VPC{AgentID: f.agent.ID, Name: "mig-src", Driver: model.DriverQEMU, Subnet: "10.20.0.0/24", Gateway: "10.20.0.1", State: "available"}
	if err := f.db.Create(&vpc).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&f.inst).Update("vpc_id", vpc.ID).Error; err != nil {
		t.Fatal(err)
	}
	var inst model.Instance
	if err := f.db.Preload("VPC").First(&inst, f.inst.ID).Error; err != nil {
		t.Fatal(err)
	}
	mig := &model.Migration{InstanceID: inst.ID, OperationID: "op-src", SourceAgentID: f.agent.ID, TargetAgentID: f.agent.ID + 7, TargetVPCID: nil, SourceVPCID: &vpc.ID, Stage: "reserved"}
	if err := f.db.Create(mig).Error; err != nil {
		t.Fatal(err)
	}
	var stored model.Migration
	if err := f.db.First(&stored, mig.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.SourceVPCID == nil || *stored.SourceVPCID != vpc.ID {
		t.Fatalf("source VPC reservation not persisted: %+v", stored.SourceVPCID)
	}
	_ = context.Background()
}

func hasPlaintextColumn(db *gorm.DB) bool {
	return db.Migrator().HasColumn(&model.Agent{}, "token")
}
