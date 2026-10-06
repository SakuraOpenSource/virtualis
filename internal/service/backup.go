package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/SakuraOpenSource/virtualis/internal/model"
	"gorm.io/gorm"
)

func archiveFormat(driver string) (string, string) {
	if driver == model.DriverIncus {
		return "tar.gz", "backup.tar.gz"
	}
	return "tar", "backup.tar"
}
func (s *VirtualisService) ListBackups(id uint) ([]model.Backup, error) {
	if _, err := s.GetInstance(id); err != nil {
		return nil, err
	}
	items := []model.Backup{}
	err := s.db.Where("instance_id = ?", id).Order("id DESC").Find(&items).Error
	return items, err
}
func (s *VirtualisService) backup(id, bid uint) (*model.Backup, error) {
	var backup model.Backup
	err := s.db.Where("instance_id = ? AND id = ?", id, bid).First(&backup).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, NotFound("backup not found")
	}
	return &backup, err
}
func (s *VirtualisService) CreateBackup(ctx context.Context, id uint, req RecoveryInput) (result *model.Backup, err error) {
	if err = validateRecoveryInput(req); err != nil {
		return nil, err
	}
	if s.storage == nil {
		return nil, Internal("backup storage unavailable")
	}
	guard, err := s.beginOperation(ctx, id, "backup_create")
	if err != nil {
		return nil, err
	}
	defer guard.finish(&err)
	ctx, cancel := context.WithTimeout(ctx, recoveryTimeout)
	defer cancel()
	inst, client, err := s.recoveryInstance(ctx, id, true)
	if err != nil {
		return nil, err
	}
	format, filename := archiveFormat(inst.Driver)
	snapshots, err := s.ListSnapshots(id)
	if err != nil {
		return nil, err
	}
	backup := &model.Backup{InstanceID: id, AgentID: *inst.AgentID, Name: req.Name, Remark: strings.TrimSpace(req.Remark), Driver: inst.Driver, Format: format, Spec: inst.Spec, ConfigJSON: inst.ConfigJSON, Snapshots: snapshots, Status: "creating"}
	if err = s.db.Create(backup).Error; err != nil {
		return nil, err
	}
	if err = guard.phase("export", "streaming archive to master storage", nil); err != nil {
		return nil, err
	}
	body, length, err := client.Export(ctx, toWireInstance(inst, inst.Image))
	if err != nil {
		return nil, s.failBackup(backup, agentFailure(err))
	}
	defer body.Close()
	path, size, _, checksum, saveErr := s.storage.SaveNamed("backups", filename, &contextReader{ctx: ctx, reader: body}, maxImageUploadSize)
	if saveErr != nil {
		return nil, s.failBackup(backup, saveErr)
	}
	if length >= 0 && size != length {
		err = fmt.Errorf("truncated archive: received %d, expected %d", size, length)
		return nil, s.failBackup(backup, joinCleanup(err, s.storage.Remove(path)))
	}
	backup.FilePath, backup.SizeBytes, backup.Checksum, backup.Status = path, size, checksum, "available"
	if err = s.db.Model(backup).Select("file_path", "size_bytes", "checksum", "status").Updates(backup).Error; err != nil {
		// Keep the durable archive identity even when final persistence failed.
		persistErr := s.db.Model(backup).Updates(map[string]any{"file_path": path, "size_bytes": size, "checksum": checksum, "status": "error", "error": err.Error()}).Error
		if persistErr != nil {
			guard.retain = true
			return nil, errors.Join(err, persistErr, fmt.Errorf("archive retained for recovery: %s", path))
		}
		return nil, err
	}
	return backup, nil
}
func (s *VirtualisService) failBackup(backup *model.Backup, cause error) error {
	persistErr := s.db.Model(backup).Updates(map[string]any{"status": "error", "error": cause.Error()}).Error
	return errors.Join(cause, persistErr)
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// OpenBackup verifies the exact stored bytes before exposing them or replacing
// a runtime. Hashing and transfer use bounded buffers, never a GiB byte slice.
func (s *VirtualisService) OpenBackup(ctx context.Context, id, bid uint) (*os.File, *model.Backup, error) {
	if _, err := s.GetInstance(id); err != nil {
		return nil, nil, err
	}
	backup, err := s.backup(id, bid)
	if err != nil {
		return nil, nil, err
	}
	if s.storage == nil || backup.Status != "available" {
		return nil, nil, Conflict("backup is unavailable")
	}
	file, err := s.storage.Open(backup.FilePath)
	if err != nil {
		return nil, nil, err
	}
	digest := sha256.New()
	n, err := io.Copy(digest, &contextReader{ctx: ctx, reader: file})
	if err == nil && (n != backup.SizeBytes || hex.EncodeToString(digest.Sum(nil)) != backup.Checksum) {
		err = Conflict("backup checksum or size mismatch")
	}
	if err == nil {
		_, err = file.Seek(0, io.SeekStart)
	}
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	return file, backup, nil
}
