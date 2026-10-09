package service

import (
	"context"
	"errors"

	"github.com/SakuraOpenSource/virtualis/internal/model"
	"gorm.io/gorm"
)

func (s *VirtualisService) RestoreSnapshot(ctx context.Context, id, sid uint) (result *model.Instance, err error) {
	guard, err := s.beginOperation(ctx, id, "snapshot_restore")
	if err != nil {
		return nil, err
	}
	// finishInstance clears the released fence from the returned instance so
	// clients do not keep rendering a completed operation as busy (the plain
	// finish helper only releases the SQL row, leaving a stale token in the
	// response payload).
	defer guard.finishInstance(&err, &result)
	ctx, cancel := context.WithTimeout(ctx, recoveryTimeout)
	defer cancel()
	snap, err := s.snapshot(id, sid)
	if err != nil {
		return nil, err
	}
	if snap.Status != "available" {
		return nil, Conflict("snapshot is not available")
	}
	inst, client, err := s.recoveryInstance(ctx, id, true)
	if err != nil {
		return nil, err
	}
	if inst.AgentID == nil || snap.AgentID != *inst.AgentID {
		return nil, Conflict("snapshot node does not match instance")
	}
	if err = guard.phase("restore", "restoring stopped instance", nil); err != nil {
		return nil, err
	}
	if _, err = client.Snapshot(ctx, toWireInstance(inst, inst.Image), snap.Name, "restore"); err != nil {
		// A failed or lost restore response is not proof that the disk stayed
		// unchanged: the agent performs its final os.Rename before replying,
		// so the replacement may already have happened. Retain the durable
		// fence until the intended recovery point is verified, mirroring the
		// backup_restore semantics.
		guard.retain = true
		return nil, agentFailure(err)
	}
	if err = s.db.Model(inst).Updates(map[string]any{"status": model.InstanceStatusStopped, "config_json": snap.ConfigJSON, "ssh_ready": false, "observed_ip": "", "lifecycle_generation": gorm.Expr("lifecycle_generation + 1")}).Error; err != nil {
		guard.retain = true
		return nil, err
	}
	return s.GetInstance(id)
}
func (s *VirtualisService) DeleteSnapshot(ctx context.Context, id, sid uint) (err error) {
	guard, err := s.beginOperation(ctx, id, "snapshot_delete")
	if err != nil {
		return err
	}
	defer guard.finish(&err)
	snap, err := s.snapshot(id, sid)
	if err != nil {
		return err
	}
	inst, client, err := s.recoveryInstance(ctx, id, false)
	if err != nil {
		return err
	}
	if _, err = client.Snapshot(ctx, toWireInstance(inst, inst.Image), snap.Name, "delete"); err != nil {
		return agentFailure(err)
	}
	return s.db.Delete(snap).Error
}

func joinCleanup(primary, cleanup error) error {
	if cleanup == nil {
		return primary
	}
	return errors.Join(primary, Conflict("compensation failed; manual recovery required: %s", cleanup))
}
