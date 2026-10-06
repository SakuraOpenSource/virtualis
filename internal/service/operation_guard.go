package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/SakuraOpenSource/virtualis/internal/model"
)

const recoveryTimeout = 2 * time.Hour

// operationGuard fences every mutating/reconciling call both in this singleton
// and in SQL. A persisted fence is deliberately never stolen on a timer: a
// crashed import may still be running and automatic expiry risks dual starts.
type operationGuard struct {
	s             *VirtualisService
	id            uint
	token, action string
	retain        bool
}

func (s *VirtualisService) beginOperation(ctx context.Context, id uint, action string) (*operationGuard, error) {
	s.opMu.Lock()
	if s.activeOperations == nil {
		s.activeOperations = make(map[uint]bool)
	}
	if s.activeOperations[id] {
		s.opMu.Unlock()
		return nil, Conflict("instance is busy")
	}
	s.activeOperations[id] = true
	s.opMu.Unlock()
	op := &operationGuard{s: s, id: id, token: newOperationID(), action: action}
	now := time.Now().UTC()
	result := s.db.WithContext(ctx).Model(&model.Instance{}).Where("id = ? AND busy_operation = ''", id).Updates(map[string]any{"busy_operation": op.token, "busy_action": action, "busy_since": now})
	if result.Error != nil || result.RowsAffected != 1 {
		op.unlockMemory()
		if result.Error != nil {
			return nil, result.Error
		}
		var count int64
		if err := s.db.Model(&model.Instance{}).Where("id = ?", id).Count(&count).Error; err != nil {
			return nil, err
		}
		if count == 0 {
			return nil, NotFound("instance not found")
		}
		return nil, Conflict("instance is busy; inspect operation logs before recovery")
	}
	if err := op.phase("start", "operation started", nil); err != nil {
		releaseErr := op.release()
		op.unlockMemory()
		return nil, errors.Join(err, releaseErr)
	}
	return op, nil
}
func (op *operationGuard) unlockMemory() {
	op.s.opMu.Lock()
	delete(op.s.activeOperations, op.id)
	op.s.opMu.Unlock()
}
func (op *operationGuard) phase(stage, message string, cause error) error {
	status := model.OperationRunning
	if cause != nil {
		status = model.OperationFailed
	}
	row := model.InstanceOperationLog{InstanceID: op.id, OperationID: op.token, Action: op.action, Stage: stage, Status: status, Message: message}
	if cause != nil {
		row.Error = cause.Error()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return op.s.db.WithContext(ctx).Create(&row).Error
}
func (op *operationGuard) release() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return op.s.db.WithContext(ctx).Model(&model.Instance{}).Where("id = ? AND busy_operation = ?", op.id, op.token).Updates(map[string]any{"busy_operation": "", "busy_action": "", "busy_since": nil}).Error
}
func (op *operationGuard) finishInstance(result *error, instance **model.Instance) {
	op.finish(result)
	if *result == nil && !op.retain && *instance != nil {
		(*instance).BusyOperation = ""
		(*instance).BusyAction = ""
		(*instance).BusySince = nil
	}
}
func (op *operationGuard) finish(result *error) {
	defer op.unlockMemory()
	row := model.InstanceOperationLog{InstanceID: op.id, OperationID: op.token, Action: op.action, Stage: "complete", Status: model.OperationSuccess, Message: "operation complete"}
	if *result != nil {
		row.Status = model.OperationFailed
		row.Message = "operation failed"
		row.Error = (*result).Error()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := op.s.db.WithContext(ctx).Create(&row).Error; err != nil {
		*result = errors.Join(*result, fmt.Errorf("persist operation log: %w", err))
		op.retain = true
	}
	if op.retain {
		message := "reconciliation required"
		if *result != nil {
			message = (*result).Error()
		}
		if err := op.s.db.WithContext(ctx).Model(&model.Instance{}).Where("id = ? AND busy_operation = ?", op.id, op.token).Update("recovery_error", message).Error; err != nil {
			*result = errors.Join(*result, err)
		}
		return
	}
	if err := op.release(); err != nil {
		*result = errors.Join(*result, fmt.Errorf("release instance fence: %w", err))
	}
}
