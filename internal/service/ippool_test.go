package service

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/SakuraOpenSource/virtualis/internal/model"
)

func newIPPoolTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "test.db")
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Windows 下文件句柄未释放会让 TempDir 清理失败，先关闭连接。
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func seedIPPoolAgent(t *testing.T, db *gorm.DB) model.Agent {
	t.Helper()
	agent := model.Agent{
		Name:      "node-01",
		Status:    model.AgentStatusOnline,
		Driver:    model.DriverIncus,
		Endpoint:  "http://127.0.0.1:8081",
		TokenHash: "placeholder-synced-by-seed",
	}
	if err := db.Create(&agent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}
	// Plaintext tokens live in the process cache, not the database (SC-03);
	// SeedRPCToken syncs the row hash so the cache/row pair RPCToken
	// verifies stays consistent.
	NewAgentService(db).SeedRPCToken(agent.ID, "test-token")
	return agent
}

func TestParseIPSpecToken(t *testing.T) {
	cases := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{in: "10.0.0.10", want: 1},
		{in: "10.0.0.10-10.0.0.12", want: 3},
		{in: "10.0.0.10 - 10.0.0.10", want: 1},
		{in: "10.0.0.12-10.0.0.10", wantErr: true},
		{in: "10.0.0.10-10.0.1.10", wantErr: true},
		{in: "not-an-ip", wantErr: true},
		{in: "10.0.0.10-abc", wantErr: true},
		{in: "2001:db8::1", wantErr: true},
	}
	for _, tc := range cases {
		got, err := parseIPSpecToken(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("parseIPSpecToken(%q) = %v, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("parseIPSpecToken(%q) error: %v", tc.in, err)
		}
		if len(got) != tc.want {
			t.Fatalf("parseIPSpecToken(%q) = %d items, want %d", tc.in, len(got), tc.want)
		}
	}
}

func TestIPPoolAddListAndFree(t *testing.T) {
	db := newIPPoolTestDB(t)
	agent := seedIPPoolAgent(t, db)
	svc := NewVirtualisService(db)

	if _, err := svc.SaveIPPoolDefaults(agent.ID, IPPoolInput{
		Gateway:   "10.0.0.1",
		Prefix:    24,
		DNS:       []string{"8.8.8.8", "1.1.1.1"},
		Interface: "eth0",
	}); err != nil {
		t.Fatalf("save defaults: %v", err)
	}

	result, err := svc.AddIPPoolEntries(agent.ID, AddIPPoolEntriesInput{
		IPs: []string{"10.0.0.10-10.0.0.12", "10.0.0.11", "bad-token"},
	})
	if err != nil {
		t.Fatalf("add entries: %v", err)
	}
	if result.Created != 3 {
		t.Fatalf("created = %d, want 3", result.Created)
	}
	if len(result.Skipped) != 2 {
		t.Fatalf("skipped = %d, want 2 (%v)", len(result.Skipped), result.Skipped)
	}

	overview, err := svc.IPPoolOverviewForAgent(agent.ID)
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if overview.Total != 3 || overview.Free != 3 || overview.Assigned != 0 {
		t.Fatalf("overview counts = total %d free %d assigned %d", overview.Total, overview.Free, overview.Assigned)
	}
	if overview.Prefix != 24 || overview.Gateway != "10.0.0.1" {
		t.Fatalf("overview defaults wrong: %+v", overview)
	}
	// Sorted ascending.
	if overview.Entries[0].IP != "10.0.0.10" || overview.Entries[2].IP != "10.0.0.12" {
		t.Fatalf("entries not sorted: %v", overview.Entries)
	}

	free, err := svc.FreeIPPoolEntries(agent.ID)
	if err != nil {
		t.Fatalf("free list: %v", err)
	}
	if len(free) != 3 {
		t.Fatalf("free = %d, want 3", len(free))
	}
	first := free[0]
	if first.CIDR != "10.0.0.10/24" || first.Gateway != "10.0.0.1" || first.Interface != "eth0" {
		t.Fatalf("effective entry wrong: %+v", first)
	}
	if len(first.DNS) != 2 {
		t.Fatalf("effective dns wrong: %+v", first.DNS)
	}
}

func TestIPPoolReserveReleaseFlow(t *testing.T) {
	db := newIPPoolTestDB(t)
	agent := seedIPPoolAgent(t, db)
	svc := NewVirtualisService(db)

	if _, err := svc.AddIPPoolEntries(agent.ID, AddIPPoolEntriesInput{IPs: []string{"10.0.0.20"}}); err != nil {
		t.Fatalf("add entries: %v", err)
	}
	entries, err := svc.FreeIPPoolEntries(agent.ID)
	if err != nil || len(entries) != 1 {
		t.Fatalf("free list: %v len=%d", err, len(entries))
	}
	entryID := entries[0].ID

	// Reserve for a fake instance.
	if err := svc.assignPoolEntry(entryID, 42); err != nil {
		t.Fatalf("assign: %v", err)
	}
	// Double reserve must fail with a conflict.
	if err := svc.assignPoolEntry(entryID, 43); err == nil {
		t.Fatalf("double assign should fail")
	}
	// The entry disappears from the free list.
	free, err := svc.FreeIPPoolEntries(agent.ID)
	if err != nil {
		t.Fatalf("free list: %v", err)
	}
	if len(free) != 0 {
		t.Fatalf("free = %d, want 0", len(free))
	}
	// Delete is refused while assigned.
	if err := svc.DeleteIPPoolEntry(entryID); err == nil {
		t.Fatalf("delete assigned entry should fail")
	}
	// Disabling an assigned entry is refused.
	disabled := model.IPPoolStatusDisabled
	if _, err := svc.UpdateIPPoolEntry(entryID, UpdateIPPoolEntryInput{Status: &disabled}); err == nil {
		t.Fatalf("disable assigned entry should fail")
	}
	// Release via instance delete path.
	if err := svc.ReleaseIPPoolInstance(42); err != nil {
		t.Fatalf("release: %v", err)
	}
	// Releasing a non-assigned instance id is a no-op.
	if err := svc.ReleaseIPPoolInstance(42); err != nil {
		t.Fatalf("release noop: %v", err)
	}
	free, err = svc.FreeIPPoolEntries(agent.ID)
	if err != nil || len(free) != 1 {
		t.Fatalf("free after release: %v len=%d", err, len(free))
	}
	// Admin escape hatch: set status free on an assigned entry clears it.
	if err := svc.assignPoolEntry(entryID, 44); err != nil {
		t.Fatalf("re-assign: %v", err)
	}
	freeStatus := model.IPPoolStatusFree
	if _, err := svc.UpdateIPPoolEntry(entryID, UpdateIPPoolEntryInput{Status: &freeStatus}); err != nil {
		t.Fatalf("force release: %v", err)
	}
	var entry model.IPPoolEntry
	if err := db.First(&entry, entryID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if entry.Status != model.IPPoolStatusFree || entry.InstanceID != nil {
		t.Fatalf("force release left state: %+v", entry)
	}
	// Now delete works.
	if err := svc.DeleteIPPoolEntry(entryID); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestIPPoolDefaultsValidation(t *testing.T) {
	db := newIPPoolTestDB(t)
	agent := seedIPPoolAgent(t, db)
	svc := NewVirtualisService(db)

	if _, err := svc.SaveIPPoolDefaults(agent.ID, IPPoolInput{Gateway: "not-an-ip"}); err == nil {
		t.Fatalf("invalid gateway should fail")
	}
	if _, err := svc.SaveIPPoolDefaults(agent.ID, IPPoolInput{Prefix: 99}); err == nil {
		t.Fatalf("invalid prefix should fail")
	}
	if _, err := svc.SaveIPPoolDefaults(agent.ID, IPPoolInput{DNS: []string{"999.9.9.9"}}); err == nil {
		t.Fatalf("invalid dns should fail")
	}
	if _, err := svc.SaveIPPoolDefaults(agent.ID, IPPoolInput{Interface: "bad iface!"}); err == nil {
		t.Fatalf("invalid interface should fail")
	}
	// Prefix 0 means "inherit default" and is allowed.
	if _, err := svc.SaveIPPoolDefaults(agent.ID, IPPoolInput{}); err != nil {
		t.Fatalf("empty defaults should save: %v", err)
	}
}

func TestIPPoolPerEntryOverride(t *testing.T) {
	db := newIPPoolTestDB(t)
	agent := seedIPPoolAgent(t, db)
	svc := NewVirtualisService(db)

	if _, err := svc.SaveIPPoolDefaults(agent.ID, IPPoolInput{Gateway: "10.0.0.1", Prefix: 24}); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if _, err := svc.AddIPPoolEntries(agent.ID, AddIPPoolEntriesInput{
		IPs:     []string{"192.168.5.8"},
		Gateway: "192.168.5.1",
		Prefix:  25,
	}); err != nil {
		t.Fatalf("add: %v", err)
	}
	free, err := svc.FreeIPPoolEntries(agent.ID)
	if err != nil || len(free) != 1 {
		t.Fatalf("free: %v len=%d", err, len(free))
	}
	if free[0].CIDR != "192.168.5.8/25" || free[0].Gateway != "192.168.5.1" {
		t.Fatalf("entry override not applied: %+v", free[0])
	}
}
