package service

import (
	"context"

	"github.com/SakuraOpenSource/virtualis/internal/model"
	"gorm.io/gorm"
)

func (s *VirtualisService) RestoreBackup(ctx context.Context, id, bid uint) (result *model.Instance, err error) {
	guard, err := s.beginOperation(ctx, id, "backup_restore")
	if err != nil {
		return nil, err
	}
	defer guard.finishInstance(&err, &result)
	ctx, cancel := context.WithTimeout(ctx, recoveryTimeout)
	defer cancel()
	file, backup, err := s.OpenBackup(ctx, id, bid)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	inst, client, err := s.recoveryInstance(ctx, id, true)
	if err != nil {
		return nil, err
	}
	if backup.Driver != inst.Driver || canonicalArch(backup.Spec.Arch) != canonicalArch(inst.Spec.Arch) {
		return nil, BadRequest("backup driver or architecture mismatch")
	}
	if backup.Spec.DiskGB > inst.Spec.DiskGB {
		return nil, BadRequest("restoring would shrink archived disk")
	}
	if err = guard.phase("import", "replacing stopped runtime using verified archive", nil); err != nil {
		return nil, err
	}
	_, filename := archiveFormat(backup.Driver)
	remote, err := client.Import(ctx, toWireInstance(inst, inst.Image), file, filename, true)
	if err != nil {
		guard.retain = true
		return nil, agentFailure(err)
	}
	verified, err := client.Status(ctx, remote)
	if err != nil || verified.Status != model.InstanceStatusStopped {
		guard.retain = true
		return nil, joinCleanup(Conflict("restored runtime needs verification"), err)
	}
	if err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(inst).Updates(map[string]any{"status": model.InstanceStatusStopped, "config_json": backup.ConfigJSON, "ssh_ready": false, "observed_ip": "", "network_error": ""}).Error; err != nil {
			return err
		}
		if err := tx.Where("instance_id = ?", id).Delete(&model.Snapshot{}).Error; err != nil {
			return err
		}
		for _, snap := range backup.Snapshots {
			if snap.Status != "available" {
				continue
			}
			snap.Base = model.Base{}
			snap.InstanceID = id
			snap.AgentID = *inst.AgentID
			if err := tx.Create(&snap).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		guard.retain = true
		return nil, err
	}
	return s.GetInstance(id)
}
func (s *VirtualisService) DeleteBackup(ctx context.Context, id, bid uint) (err error) {
	guard, err := s.beginOperation(ctx, id, "backup_delete")
	if err != nil {
		return err
	}
	defer guard.finish(&err)
	if _, err = s.GetInstance(id); err != nil {
		return err
	}
	backup, err := s.backup(id, bid)
	if err != nil {
		return err
	}
	if s.storage == nil {
		return Internal("backup storage unavailable")
	}
	// Keep the row on filesystem failure. If SQL fails after unlink, the same
	// request can retry: missing files are idempotent in Store.Remove.
	if backup.FilePath != "" {
		if err = s.storage.Remove(backup.FilePath); err != nil {
			return err
		}
	}
	return s.db.Delete(backup).Error
}
func canonicalArch(arch string) string {
	switch arch {
	case "amd64", "x86_64":
		return "x86_64"
	case "arm64", "aarch64":
		return "aarch64"
	}
	return arch
}
