package service

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/SakuraOpenSource/virtualis/internal/agentclient"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"gorm.io/gorm"
)

var recoveryNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

type RecoveryInput struct {
	Name   string `json:"name"`
	Remark string `json:"remark"`
}

func validateRecoveryInput(req RecoveryInput) error {
	if !recoveryNamePattern.MatchString(req.Name) {
		return BadRequest("name must be 1-64 letters, digits, underscores or hyphens")
	}
	if len(req.Remark) > 255 {
		return BadRequest("remark too long")
	}
	return nil
}
func (s *VirtualisService) ListSnapshots(id uint) ([]model.Snapshot, error) {
	if _, err := s.GetInstance(id); err != nil {
		return nil, err
	}
	items := []model.Snapshot{}
	err := s.db.Where("instance_id = ?", id).Order("id DESC").Find(&items).Error
	return items, err
}
func (s *VirtualisService) recoveryInstance(ctx context.Context, id uint, stopped bool) (*model.Instance, *agentclient.Client, error) {
	inst, err := s.GetInstance(id)
	if err != nil {
		return nil, nil, err
	}
	client, err := s.agentClient(inst.Agent)
	if err != nil {
		return nil, nil, err
	}
	if stopped {
		remote, err := client.Status(ctx, toWireInstance(inst, inst.Image))
		if err != nil {
			return nil, nil, agentFailure(err)
		}
		if remote.Status != model.InstanceStatusStopped {
			return nil, nil, Conflict("stop instance before this operation")
		}
		inst.Status = model.InstanceStatusStopped
	}
	return inst, client, nil
}
func (s *VirtualisService) CreateSnapshot(ctx context.Context, id uint, req RecoveryInput) (result *model.Snapshot, err error) {
	if err = validateRecoveryInput(req); err != nil {
		return nil, err
	}
	guard, err := s.beginOperation(ctx, id, "snapshot_create")
	if err != nil {
		return nil, err
	}
	defer guard.finish(&err)
	ctx, cancel := context.WithTimeout(ctx, recoveryTimeout)
	defer cancel()
	inst, client, err := s.recoveryInstance(ctx, id, false)
	if err != nil {
		return nil, err
	}
	snap := &model.Snapshot{InstanceID: id, AgentID: *inst.AgentID, Name: req.Name, Remark: strings.TrimSpace(req.Remark), Status: "creating", ConfigJSON: inst.ConfigJSON}
	var count int64
	if err = s.db.Model(&model.Snapshot{}).Where("instance_id = ? AND name = ?", id, req.Name).Count(&count).Error; err != nil {
		return nil, err
	}
	if count > 0 {
		return nil, Conflict("snapshot name already exists")
	}
	if err = s.db.Create(snap).Error; err != nil {
		return nil, err
	}
	size, remoteErr := client.Snapshot(ctx, toWireInstance(inst, inst.Image), req.Name, "create")
	if remoteErr != nil {
		persistErr := s.db.Model(snap).Updates(map[string]any{"status": "error", "error": remoteErr.Error()}).Error
		return nil, errors.Join(agentFailure(remoteErr), persistErr)
	}
	snap.SizeBytes, snap.Status = size, "available"
	if err = s.db.Model(snap).Updates(map[string]any{"size_bytes": size, "status": "available"}).Error; err != nil {
		// The durable creating record identifies an uncertain snapshot for retry or
		// deletion. Never remove the guest or unrelated snapshots as compensation.
		return nil, err
	}
	return snap, nil
}
func (s *VirtualisService) snapshot(id, sid uint) (*model.Snapshot, error) {
	var snap model.Snapshot
	err := s.db.Where("instance_id = ? AND id = ?", id, sid).First(&snap).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, NotFound("snapshot not found")
	}
	return &snap, err
}
