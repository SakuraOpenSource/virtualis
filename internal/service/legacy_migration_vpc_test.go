package service

import (
	"context"
	"testing"

	"github.com/SakuraOpenSource/virtualis/internal/database"
	"github.com/SakuraOpenSource/virtualis/internal/model"
)

// REV-MIGRATION-LEGACY-VPC: migrations recorded BEFORE the source_vpc_id
// column existed have NULL there. A retained (non-terminal) legacy
// migration still pins its source network for manual recovery, but
// DeleteVPC's query only consults source_vpc_id and target_vpc_id, so after
// an upgrade the operator can delete the VPC the suspended migration still
// needs. The upgrade data migration must backfill source references for
// legacy retained rows from the instance row itself.
func TestLegacyRetainedMigrationStillPinsSourceVPC(t *testing.T) {
	f := newLifecycleFixture(t, nil)
	vpc := model.VPC{AgentID: f.agent.ID, Name: "legacy-src", Driver: model.DriverQEMU, Subnet: "10.30.0.0/24", Gateway: "10.30.0.1", State: "available"}
	if err := f.db.Create(&vpc).Error; err != nil {
		t.Fatal(err)
	}
	// Historical state: the instance still points at the source VPC and a
	// retained migration exists with stage=source_delete, but its
	// source_vpc_id is NULL (pre-upgrade schema never wrote it).
	if err := f.db.Model(&f.inst).Update("vpc_id", vpc.ID).Error; err != nil {
		t.Fatal(err)
	}
	legacy := &model.Migration{
		InstanceID:    f.inst.ID,
		OperationID:   "legacy-op-1",
		SourceAgentID: f.agent.ID,
		TargetAgentID: f.agent.ID + 9,
		Stage:         "source_delete",
	}
	if err := f.db.Create(legacy).Error; err != nil {
		t.Fatal(err)
	}

	// The upgrade path: database.Migrate runs AutoMigrate plus the legacy
	// backfill data migration, exactly as a real upgrade would.
	if err := database.Migrate(f.db); err != nil {
		t.Fatalf("upgrade migrate: %v", err)
	}
	var stored model.Migration
	if err := f.db.First(&stored, legacy.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.SourceVPCID == nil || *stored.SourceVPCID != vpc.ID {
		t.Fatalf("legacy source VPC not backfilled: %+v", stored.SourceVPCID)
	}

	// DeleteVPC must refuse while the (now referenced) source network is
	// pinned by the retained migration.
	if err := f.svc.DeleteVPC(context.Background(), vpc.ID); err == nil {
		t.Fatal("source VPC deletable under a retained legacy migration")
	}
}

// REV-MIGRATION-LEGACY-VPC (unresolvable rows): a legacy retained migration
// whose source cannot be identified must BLOCK deletion of candidate source
// networks on its recorded source agent rather than being treated as
// reference-free.
func TestUnresolvableLegacyMigrationBlocksSourceAgentVPCDelete(t *testing.T) {
	f := newLifecycleFixture(t, nil)
	// Legacy retained migration: no source_vpc_id, and the instance no
	// longer points at any VPC (DB switch already happened historically).
	legacy := &model.Migration{
		InstanceID:    f.inst.ID,
		OperationID:   "legacy-op-2",
		SourceAgentID: f.agent.ID,
		TargetAgentID: f.agent.ID + 9,
		Stage:         "switching",
	}
	if err := f.db.Create(legacy).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(f.db); err != nil {
		t.Fatalf("upgrade migrate: %v", err)
	}
	// A VPC on the recorded source agent must be undeletable: the legacy
	// migration's unknown source may be exactly this network.
	src := model.VPC{AgentID: f.agent.ID, Name: "maybe-source", Driver: model.DriverQEMU, Subnet: "10.40.0.0/24", Gateway: "10.40.0.1", State: "available"}
	if err := f.db.Create(&src).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.svc.DeleteVPC(context.Background(), src.ID); err == nil {
		t.Fatal("source-agent VPC deletable under an unresolvable legacy migration")
	}
}
