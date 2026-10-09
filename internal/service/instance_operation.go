package service

import (
	"crypto/rand"
	"encoding/hex"

	"gorm.io/gorm"

	"github.com/SakuraOpenSource/virtualis/internal/model"
)

// newOperationID creates a compact correlation id shared by every stage row.
func newOperationID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "operation"
	}
	return hex.EncodeToString(buf)
}

func appendOperationLog(db *gorm.DB, instanceID uint, operationID, action, stage, status, message string, err error) {
	row := model.InstanceOperationLog{
		InstanceID:  instanceID,
		OperationID: operationID,
		Action:      action,
		Stage:       stage,
		Status:      status,
		Message:     message,
	}
	if err != nil {
		row.Error = err.Error()
	}
	// Logging must never turn a successful lifecycle operation into failure.
	_ = db.Create(&row).Error
}

// InstanceOperationLogs returns recent logs, newest first.
//
// Trashed instances are deliberately included: this is a read-only
// diagnostics endpoint, and the recycle bin is exactly where a retained
// recovery fence strands an instance — refusing logs there would hide the
// persisted operation history the operator needs to decide on manual
// recovery. Ownership/authorization is still enforced by the route middleware
// (InstanceOwnership consults the raw row, not GetInstance).
//
// There is intentionally no supported "release the durable fence" primitive:
// the fence exists because a crashed operation may still be running on the
// node and automatic or casual unlocking risks dual starts. Supported
// recovery is manual and staged: an administrator verifies on the node that
// the runtime is stopped and the disk state matches the intended recovery
// point, then clears busy_operation for that single row in the database
// while the master service is stopped. Never clear it just because time
// passed.
func (s *VirtualisService) InstanceOperationLogs(instanceID uint, offset, limit int) ([]model.InstanceOperationLog, int64, error) {
	if _, err := s.GetAnyInstance(instanceID); err != nil {
		return nil, 0, err
	}
	var total int64
	query := s.db.Model(&model.InstanceOperationLog{}).Where("instance_id = ?", instanceID)
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []model.InstanceOperationLog
	err := query.Order("id DESC").Offset(offset).Limit(limit).Find(&items).Error
	return items, total, err
}
