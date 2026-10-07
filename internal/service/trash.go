package service

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/SakuraOpenSource/virtualis/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const KeyTrashRetentionDays = "virtualis_trash_retention_days"

type RetentionSettings struct {
	RetentionDays int `json:"retention_days"`
}

func (s *VirtualisService) Retention() (RetentionSettings, error) {
	var row model.Setting
	err := s.db.Where("key = ?", KeyTrashRetentionDays).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return RetentionSettings{RetentionDays: 30}, nil
	}
	if err != nil {
		return RetentionSettings{}, err
	}
	days, err := strconv.Atoi(row.Value)
	if err != nil || days < 0 || days > 3650 {
		return RetentionSettings{}, Internal("invalid retention setting")
	}
	return RetentionSettings{RetentionDays: days}, nil
}
func (s *VirtualisService) SaveRetention(req RetentionSettings) (RetentionSettings, error) {
	if req.RetentionDays < 0 || req.RetentionDays > 3650 {
		return req, BadRequest("retention_days must be 0-3650; 0 disables automatic purge")
	}
	err := s.db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"value"})}).Create(&model.Setting{Key: KeyTrashRetentionDays, Value: strconv.Itoa(req.RetentionDays)}).Error
	return req, err
}
func (s *VirtualisService) DeleteInstance(ctx context.Context, id uint) (err error) {
	guard, err := s.beginOperation(ctx, id, "recycle")
	if err != nil {
		return err
	}
	defer guard.finish(&err)
	inst, err := s.GetAnyInstance(id)
	if err != nil {
		return err
	}
	if inst.TrashedAt != nil {
		return nil
	}
	settings, err := s.Retention()
	if err != nil {
		return err
	}
	if inst.AgentID != nil {
		client, err := s.agentClient(inst.Agent)
		if err != nil {
			return err
		}
		remote, err := client.Status(ctx, toWireInstance(inst, inst.Image))
		if err != nil {
			return agentFailure(err)
		}
		if remote.Status != model.InstanceStatusStopped {
			if err = guard.phase("stop", "stopping runtime before recycling", nil); err != nil {
				return err
			}
			remote, err = client.PowerInstance(ctx, toWireInstance(inst, inst.Image), model.ActionStop, nil, nil, "", nil, "")
			if err != nil {
				return agentFailure(err)
			}
			if remote.Status != model.InstanceStatusStopped {
				return Conflict("Agent did not confirm stopped runtime")
			}
		}
	}
	now := time.Now().UTC()
	var after *time.Time
	if settings.RetentionDays > 0 {
		value := now.Add(time.Duration(settings.RetentionDays) * 24 * time.Hour)
		after = &value
	}
	return s.db.Model(inst).Where("busy_operation = ?", guard.token).Updates(map[string]any{"trashed_at": now, "purge_after": after, "status": model.InstanceStatusStopped, "ssh_ready": false}).Error
}
func (s *VirtualisService) ListTrash(page, size int, ownerID uint) ([]model.Instance, int64, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 20
	}
	if size > 100 {
		size = 100
	}
	query := s.db.Model(&model.Instance{}).Where("trashed_at IS NOT NULL")
	if ownerID != 0 {
		query = query.Where("owner_id = ?", ownerID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return nil, 0, err
	}
	items := []model.Instance{}
	err := query.Preload("Agent").Order("trashed_at DESC").Offset((page - 1) * size).Limit(size).Find(&items).Error
	return items, count, err
}
func (s *VirtualisService) RestoreTrashedInstance(ctx context.Context, id uint) (result *model.Instance, err error) {
	guard, err := s.beginOperation(ctx, id, "trash_restore")
	if err != nil {
		return nil, err
	}
	defer guard.finish(&err)
	inst, err := s.GetAnyInstance(id)
	if err != nil {
		return nil, err
	}
	if inst.TrashedAt == nil {
		return nil, Conflict("instance is not in recycle bin")
	}
	if inst.AgentID != nil {
		client, err := s.agentClient(inst.Agent)
		if err != nil {
			return nil, err
		}
		remote, err := client.Status(ctx, toWireInstance(inst, inst.Image))
		if err != nil {
			return nil, agentFailure(err)
		}
		if remote.Status != model.InstanceStatusStopped {
			return nil, Conflict("recycled runtime must be stopped before restoration")
		}
	}
	if err = s.db.Model(inst).Updates(map[string]any{"trashed_at": nil, "purge_after": nil, "status": model.InstanceStatusStopped}).Error; err != nil {
		return nil, err
	}
	return s.GetInstance(id)
}
func (s *VirtualisService) PurgeInstance(ctx context.Context, id uint) (err error) {
	guard, err := s.beginOperation(ctx, id, "purge")
	if err != nil {
		return err
	}
	defer guard.finish(&err)
	inst, err := s.GetAnyInstance(id)
	if err != nil {
		return err
	}
	if err = guard.phase("runtime_delete", "permanent runtime deletion", nil); err != nil {
		return err
	}
	if inst.AgentID != nil {
		client, err := s.agentClient(inst.Agent)
		if err != nil {
			return err
		}
		if err = client.DeleteInstance(ctx, toWireInstance(inst, inst.Image)); err != nil {
			return agentFailure(err)
		}
	}
	if err = s.db.Model(inst).Update("status", model.InstanceStatusDeleting).Error; err != nil {
		return err
	}
	var backups []model.Backup
	if err = s.db.Where("instance_id = ?", id).Find(&backups).Error; err != nil {
		return err
	}
	for _, backup := range backups {
		if backup.FilePath != "" {
			if s.storage == nil {
				return Internal("backup storage unavailable")
			}
			if err = s.storage.Remove(backup.FilePath); err != nil {
				return err
			}
		}
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		for _, entity := range []any{&model.NATMapping{}, &model.FirewallRule{}, &model.Snapshot{}, &model.Backup{}, &model.InstanceSecurityGroup{}} {
			if err := tx.Where("instance_id = ?", id).Delete(entity).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&model.IPPoolEntry{}).Where("instance_id = ?", id).Updates(map[string]any{"instance_id": nil, "assigned_at": nil, "status": model.IPPoolStatusFree}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.Instance{}, id).Error
	})
}

type BatchFailure struct {
	ID     uint   `json:"id"`
	Reason string `json:"reason"`
}
type BatchResult struct {
	OK     []uint         `json:"ok"`
	Failed []BatchFailure `json:"failed"`
}

func (s *VirtualisService) CleanupTrash(ctx context.Context, now time.Time, limit int) (BatchResult, error) {
	out := BatchResult{OK: []uint{}, Failed: []BatchFailure{}}
	// retention_days=0 表示关闭自动清理：调度器不得触碰任何回收站实例。
	retention, err := s.Retention()
	if err != nil {
		return out, err
	}
	if retention.RetentionDays == 0 {
		return out, nil
	}
	if limit < 1 || limit > 100 {
		limit = 100
	}
	var ids []uint
	query := s.db.Model(&model.Instance{}).Where("trashed_at IS NOT NULL AND purge_after IS NOT NULL")
	if s.db.Dialector.Name() == "sqlite" {
		query = query.Where("julianday(purge_after) <= julianday(?)", now).Order("julianday(purge_after) ASC")
	} else {
		query = query.Where("purge_after <= ?", now).Order("purge_after ASC")
	}
	if err := query.Limit(limit).Pluck("id", &ids).Error; err != nil {
		return out, err
	}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if err := s.PurgeInstance(ctx, id); err != nil {
			out.Failed = append(out.Failed, BatchFailure{ID: id, Reason: err.Error()})
		} else {
			out.OK = append(out.OK, id)
		}
	}
	return out, nil
}
