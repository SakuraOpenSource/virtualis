package service

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"gorm.io/gorm"
	"time"
)

type MigrationInput struct {
	TargetAgentID uint                 `json:"target_agent_id"`
	VPCID         *uint                `json:"vpc_id,omitempty"`
	IPPoolEntryID *uint                `json:"ip_pool_entry_id,omitempty"`
	Network       *model.NetworkConfig `json:"network,omitempty"`
	// CallerRef carries the upstream correlation id (X-Levis-Operation-ID)
	// as a durable idempotency key. Set by the transport layer only.
	CallerRef string `json:"-"`
}

func (s *VirtualisService) MigrateInstance(ctx context.Context, id uint, req MigrationInput) (result *model.Instance, err error) {
	guard, err := s.beginOperationWithRef(ctx, id, "migrate", req.CallerRef)
	if err != nil {
		return nil, err
	}
	if guard.replay {
		// Idempotent retry of a completed migration.
		return s.GetInstance(id)
	}
	defer guard.finishInstance(&err, &result)
	ctx, cancel := context.WithTimeout(ctx, recoveryTimeout)
	defer cancel()
	source, sourceClient, err := s.recoveryInstance(ctx, id, true)
	if err != nil {
		return nil, err
	}
	if req.TargetAgentID == 0 || req.TargetAgentID == *source.AgentID {
		return nil, BadRequest("choose a different target Agent")
	}
	targetAgent, err := NewAgentService(s.db).Get(req.TargetAgentID)
	if err != nil {
		return nil, err
	}
	if !targetAgent.IsOnline() || canonicalArch(targetAgent.Arch) != canonicalArch(source.Spec.Arch) {
		return nil, BadRequest("target must be online and support source architecture")
	}
	targetClient, err := s.agentClient(targetAgent)
	if err != nil {
		return nil, err
	}
	drivers, err := targetClient.Drivers(ctx)
	if err != nil {
		return nil, agentFailure(err)
	}
	if !capabilityAvailable(drivers, source.Driver) {
		return nil, BadRequest("target driver unavailable")
	}
	if s.storage == nil {
		return nil, Internal("migration storage unavailable")
	}
	target := *source
	target.AgentID = &targetAgent.ID
	target.Agent = targetAgent
	target.VPCID = req.VPCID
	target.VPC = nil
	target.ObservedIP = ""
	target.SSHReady = false
	target.Network = model.NetworkConfig{Mode: source.Network.Mode, BandwidthMbps: source.Network.BandwidthMbps, TrafficGB: source.Network.TrafficGB}
	if req.Network != nil {
		target.Network = *req.Network
	}
	if req.VPCID != nil {
		vpc, e := s.GetVPC(*req.VPCID)
		if e != nil {
			return nil, e
		}
		if vpc.AgentID != targetAgent.ID || vpc.Driver != source.Driver || target.Network.Mode != model.NetworkModeVPC {
			return nil, BadRequest("target VPC driver/ownership mismatch")
		}
		target.VPC = vpc
		target.Network.Bridge = vpc.Name
		target.Network.Gateway = vpc.Gateway
		if len(target.Network.DNS) == 0 {
			target.Network.DNS = append([]string(nil), vpc.DNS...)
		}
	} else if target.Network.Mode == model.NetworkModeVPC {
		return nil, BadRequest("select target VPC")
	}
	if target.Network.Mode == model.NetworkModeDedicated {
		if req.IPPoolEntryID == nil {
			return nil, BadRequest("dedicated migration requires a target pool address")
		}
		entry, pool, e := s.poolEntryForCreate(targetAgent.ID, *req.IPPoolEntryID)
		if e != nil {
			return nil, e
		}
		effective := effectiveFreeEntry(*entry, *pool)
		if target.Network.IPv4 != "" && !sameIPv4(target.Network.IPv4, entry.IP) {
			return nil, BadRequest("target pool address mismatch")
		}
		target.Network.IPv4 = effective.CIDR
		if target.Network.Gateway == "" {
			target.Network.Gateway = effective.Gateway
		}
		if len(target.Network.DNS) == 0 {
			target.Network.DNS = effective.DNS
		}
		if target.Network.Bridge == "" {
			target.Network.Bridge = effective.Interface
		}
		hn, e := targetClient.HostNetwork(ctx)
		if e != nil {
			return nil, agentFailure(e)
		}
		if e = validateDedicatedHost(target.Network, hn); e != nil {
			return nil, e
		}
		taken, e := s.dedicatedIPTaken(targetAgent.ID, target.Network.IPv4, id)
		if e != nil {
			return nil, e
		}
		if taken {
			return nil, Conflict("target IP is already in use")
		}
	} else if req.IPPoolEntryID != nil {
		return nil, BadRequest("pool selection requires dedicated mode")
	}
	target.Network, err = model.NormalizeNetworkConfig(target.Network)
	if err != nil {
		return nil, BadRequest("%s", err)
	}
	if target.Network.Mode != model.NetworkModeNAT && len(source.NATMappings) > 0 {
		return nil, BadRequest("migrate NAT mappings only to NAT mode")
	}
	target.NATMappings = nil
	sourceJSON, _ := json.Marshal(toWireInstance(source, source.Image))
	// SourceVPCID reserves the source network across the whole migration: the
	// DB switch clears Instance.vpc_id before the source delete is confirmed,
	// and a fenced migration may still need its source runtime (and therefore
	// its source VPC) for manual recovery. DeleteVPC consults it.
	migration := &model.Migration{InstanceID: id, OperationID: guard.token, SourceAgentID: *source.AgentID, TargetAgentID: targetAgent.ID, TargetVPCID: req.VPCID, SourceVPCID: source.VPCID, TargetPoolEntryID: req.IPPoolEntryID, SourceJSON: string(sourceJSON), Stage: "reserved"}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if req.VPCID != nil {
			if e := reserveVPCReference(tx, *req.VPCID); e != nil {
				return e
			}
		}
		if req.IPPoolEntryID != nil {
			now := time.Now()
			res := tx.Model(&model.IPPoolEntry{}).Where("id = ? AND agent_id = ? AND status = ? AND instance_id IS NULL", *req.IPPoolEntryID, targetAgent.ID, model.IPPoolStatusFree).Updates(map[string]any{"status": model.IPPoolStatusAssigned, "instance_id": id, "assigned_at": now})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected != 1 {
				return Conflict("target pool address raced")
			}
		}
		for _, old := range source.NATMappings {
			port, e := NewVirtualisService(tx).allocateNATPort(targetAgent.ID)
			if e != nil {
				return e
			}
			row := old
			row.Base = model.Base{}
			row.AgentID = targetAgent.ID
			row.HostPort = port
			row.ReservationOperation = guard.token
			if e = tx.Create(&row).Error; e != nil {
				return e
			}
			target.NATMappings = append(target.NATMappings, row)
		}
		raw, _ := json.Marshal(toWireInstance(&target, target.Image))
		migration.TargetJSON = string(raw)
		return tx.Create(migration).Error
	})
	if err != nil {
		return nil, err
	}
	// Prior to a successful import the Agent owns cleanup of its own partial
	// creations. Master must never generic-delete a target on an import error.
	imported := false
	preserve := false
	defer func() {
		if err == nil {
			return
		}
		if preserve {
			guard.retain = true
			err = errors.Join(err, s.db.Model(migration).Update("error", err.Error()).Error)
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), recoveryTimeout)
		defer cleanupCancel()
		if imported {
			if e := targetClient.DeleteInstance(cleanupCtx, toWireInstance(&target, target.Image)); e != nil {
				guard.retain = true
				err = errors.Join(err, e, s.db.Model(migration).Updates(map[string]any{"stage": "target_cleanup_failed", "error": e.Error()}).Error)
				return
			}
		}
		e := s.releaseMigrationReservations(migration, false)
		err = errors.Join(err, e)
		if e != nil {
			guard.retain = true
			return
		}
		if migration.ArchivePath != "" {
			err = errors.Join(err, s.storage.Remove(migration.ArchivePath))
		}
		err = errors.Join(err, s.db.Model(migration).Updates(map[string]any{"stage": "failed", "error": err.Error()}).Error)
	}()
	if err = guard.phase("export", "exporting stopped source", nil); err != nil {
		return nil, err
	}
	body, length, err := sourceClient.Export(ctx, toWireInstance(source, source.Image))
	if err != nil {
		return nil, agentFailure(err)
	}
	_, filename := archiveFormat(source.Driver)
	path, size, _, checksum, saveErr := s.storage.SaveNamed("migrations", filename, &contextReader{ctx: ctx, reader: body}, maxImageUploadSize)
	closeErr := body.Close()
	if saveErr != nil {
		return nil, errors.Join(saveErr, closeErr)
	}
	migration.ArchivePath = path
	migration.SizeBytes = size
	migration.Checksum = checksum
	if closeErr != nil || length >= 0 && size != length {
		return nil, errors.Join(closeErr, Conflict("truncated migration archive"))
	}
	if err = s.db.Model(migration).Updates(map[string]any{"archive_path": path, "size_bytes": size, "checksum": checksum, "stage": "importing"}).Error; err != nil {
		return nil, err
	}
	if err = guard.phase("import", "importing target without replacing any runtime", nil); err != nil {
		return nil, err
	}
	file, err := s.storage.Open(path)
	if err != nil {
		return nil, err
	}
	remote, importErr := targetClient.Import(ctx, toWireInstance(&target, target.Image), file, filename, false)
	closeErr = file.Close()
	if importErr != nil {
		preserve = true
		return nil, errors.Join(agentFailure(importErr), closeErr)
	}
	imported = true
	if closeErr != nil {
		return nil, closeErr
	}
	if err = guard.phase("verify", "verifying stopped target identity", nil); err != nil {
		return nil, err
	}
	verified, err := targetClient.Status(ctx, remote)
	if err != nil {
		return nil, agentFailure(err)
	}
	if verified.ID != id || verified.Driver != source.Driver || verified.Status != model.InstanceStatusStopped || canonicalArch(verified.Spec.Arch) != canonicalArch(source.Spec.Arch) {
		return nil, Conflict("target verification failed")
	}
	mergeRemoteNetwork(&target, verified)
	networkJSON, _ := json.Marshal(target.Network)
	raw, _ := json.Marshal(toWireInstance(&target, target.Image))
	migration.TargetJSON = string(raw)
	if err = s.db.Model(migration).Updates(map[string]any{"stage": "switching", "target_json": migration.TargetJSON}).Error; err != nil {
		preserve = true
		return nil, err
	}
	preserve = true // Once switch starts, retain both identities and fence on failure.
	if err = guard.phase("database_switch", "switching ownership before source deletion", nil); err != nil {
		return nil, err
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.Instance{}).Where("id = ? AND agent_id = ? AND busy_operation = ?", id, *source.AgentID, guard.token).Updates(map[string]any{"agent_id": targetAgent.ID, "vpc_id": req.VPCID, "network": string(networkJSON), "ip": primaryConfiguredIP(target.Network), "observed_ip": "", "ssh_ready": false, "status": model.InstanceStatusStopped, "lifecycle_generation": gorm.Expr("lifecycle_generation + 1")})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return Conflict("migration ownership CAS failed")
		}
		for _, entity := range []any{&model.Snapshot{}, &model.FirewallRule{}} {
			if e := tx.Model(entity).Where("instance_id = ?", id).Update("agent_id", targetAgent.ID).Error; e != nil {
				return e
			}
		}
		if e := tx.Model(&model.NATMapping{}).Where("instance_id = ? AND agent_id = ? AND reservation_operation = ''", id, *source.AgentID).Update("reservation_operation", "source-"+guard.token).Error; e != nil {
			return e
		}
		if e := tx.Model(&model.NATMapping{}).Where("reservation_operation = ?", guard.token).Update("reservation_operation", "").Error; e != nil {
			return e
		}
		return tx.Model(migration).Update("stage", "source_delete").Error
	})
	if err != nil {
		return nil, err
	}
	if err = guard.phase("source_delete", "deleting source last", nil); err != nil {
		return nil, err
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), recoveryTimeout)
	defer cleanupCancel()
	if err = sourceClient.DeleteInstance(cleanupCtx, toWireInstance(source, source.Image)); err != nil {
		return nil, agentFailure(err)
	}
	if err = s.releaseMigrationReservations(migration, true); err != nil {
		return nil, err
	}
	if err = s.storage.Remove(path); err != nil {
		return nil, err
	}
	preserve = false
	return s.GetInstance(id)
}

func (s *VirtualisService) releaseMigrationReservations(m *model.Migration, switched bool) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if switched {
			query := tx.Model(&model.IPPoolEntry{}).Where("instance_id = ? AND agent_id = ?", m.InstanceID, m.SourceAgentID)
			if e := query.Updates(map[string]any{"instance_id": nil, "assigned_at": nil, "status": model.IPPoolStatusFree}).Error; e != nil {
				return e
			}
			if e := tx.Where("reservation_operation = ?", "source-"+m.OperationID).Delete(&model.NATMapping{}).Error; e != nil {
				return e
			}
			return tx.Model(m).Update("stage", "completed").Error
		}
		if m.TargetPoolEntryID != nil {
			if e := tx.Model(&model.IPPoolEntry{}).Where("id = ? AND instance_id = ?", *m.TargetPoolEntryID, m.InstanceID).Updates(map[string]any{"instance_id": nil, "assigned_at": nil, "status": model.IPPoolStatusFree}).Error; e != nil {
				return e
			}
		}
		return tx.Where("reservation_operation = ?", m.OperationID).Delete(&model.NATMapping{}).Error
	})
}

func reserveVPCReference(tx *gorm.DB, id uint) error {
	// Conditional write takes a database row lock through reference insertion.
	res := tx.Model(&model.VPC{}).Where("id = ? AND state = ?", id, "available").Update("updated_at", time.Now())
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return Conflict("VPC unavailable")
	}
	return nil
}

func (s *VirtualisService) ListMigrations(id uint) ([]model.Migration, error) {
	if _, err := s.GetAnyInstance(id); err != nil {
		return nil, err
	}
	items := []model.Migration{}
	err := s.db.Where("instance_id = ?", id).Order("id DESC").Find(&items).Error
	return items, err
}
