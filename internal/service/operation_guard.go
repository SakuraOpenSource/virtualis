package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/SakuraOpenSource/virtualis/internal/model"
)

const recoveryTimeout = 2 * time.Hour

// operationGuard fences every mutating/reconciling call both in this singleton
// and in SQL. A persisted fence is deliberately never stolen on a timer: a
// crashed import may still be running and automatic expiry risks dual starts.
//
// Supported recovery for a retained fence (retain=true or master crash):
// there is no API unlock primitive by design — the operator must (1) read
// GET /instances/:id/logs to identify the operation and its token, (2)
// verify ON THE NODE that the runtime is stopped and the disk matches the
// intended recovery point, (3) stop the master, clear busy_operation for that
// single instance row, and restart. Unconditional or timed unlocking is
// explicitly unsupported.
type operationGuard struct {
	s             *VirtualisService
	id            uint
	token, action string
	retain        bool
	// callerRef is the optional caller-supplied correlation id (e.g. the
	// plugin's X-Levis-Operation-ID). When present it doubles as a durable
	// idempotency key: the (caller id, instance, action) triple is claimed
	// atomically in beginOperation, so a retry with the same id replays the
	// recorded outcome instead of re-running the operation. It is stored in
	// its own 128-byte column — never concatenated into the 64-byte
	// operation_id log column, which composing used to overflow on
	// PostgreSQL/MySQL. Empty for purely internal operations.
	callerRef string
	// replay marks a guard that answered from an existing record: the
	// operation body must be skipped entirely.
	replay bool
	// replayPayload carries the recorded outcome for replayed requests.
	replayPayload string
}

// setCallerRef records a caller correlation id on the guard. It must be
// called before any phase log is written. Control characters are stripped
// because the value comes from a request header and lands in persisted,
// admin-visible rows. The ceiling matches the plugin's validOperationID
// bound (128) so a legal upstream id is never silently truncated.
func (op *operationGuard) setCallerRef(ref string) {
	ref = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, ref)
	if len(ref) > 128 {
		ref = ref[:128]
	}
	op.callerRef = ref
}

// operationID returns the persisted log identity: the guard token alone.
// The caller correlation id intentionally does NOT participate: composing
// "<token>/<caller>" reached 81 bytes against the varchar(64) column and
// made PostgreSQL/MySQL log writes fail AFTER the remote work had already
// been applied, retaining the fence on every such operation. The caller id
// lives in its own column on the InstanceOperation record instead.
func (op *operationGuard) operationID() string {
	return op.token
}

func (s *VirtualisService) beginOperation(ctx context.Context, id uint, action string) (*operationGuard, error) {
	return s.beginOperationWithRef(ctx, id, action, "")
}

// beginOperationWithRef claims the lifecycle fence for (instance, action)
// under an optional caller idempotency key (X-Levis-Operation-ID).
//
// Idempotency contract: the (caller id, instance, action) triple is claimed
// atomically through a unique index BEFORE any remote side effect.
//   - no existing record  -> claim and run;
//   - existing SUCCEEDED record -> replay guard: the operation body is
//     skipped and the recorded outcome is returned (upstream retry after a
//     lost response must not re-run destructive RPCs);
//   - existing FAILED record -> the previous attempt terminally failed
//     before/at a known point; the claim is released and the caller gets a
//     plain conflict so it can retry with a new id;
//   - existing RUNNING record whose fence is still held -> busy conflict;
//   - same id, different action -> 409 protocol violation;
//   - existing RUNNING record whose fence is GONE (crash-restart or
//     retained-fence recovery cleared it) -> re-claim under the SAME guard
//     token so a deterministic retry can complete the interrupted work
//     instead of being permanently 409'd.
func (s *VirtualisService) beginOperationWithRef(ctx context.Context, id uint, action string, callerRef string) (*operationGuard, error) {
	callerRef = sanitizeCallerRef(callerRef)
	if callerRef != "" {
		var existing model.InstanceOperation
		err := s.db.WithContext(ctx).Where("caller_operation_id = ? AND instance_id = ?", callerRef, id).First(&existing).Error
		if err == nil {
			if existing.Action != action {
				return nil, Conflict("operation id %q already used for action %q", callerRef, existing.Action)
			}
			switch existing.State {
			case model.OperationStateSucceeded:
				return &operationGuard{s: s, id: id, token: existing.GuardToken, action: action, callerRef: callerRef, replay: true, replayPayload: existing.ResultPayload}, nil
			case model.OperationStateFailed:
				return nil, Conflict("operation %q previously failed; retry with a new operation id", callerRef)
			default: // running
				var count int64
				if err := s.db.Model(&model.Instance{}).Where("id = ? AND busy_operation = ?", id, existing.GuardToken).Count(&count).Error; err != nil {
					return nil, err
				}
				if count == 1 {
					return nil, Conflict("instance is busy; inspect operation logs before recovery")
				}
				// Fence gone (crash/recovery): re-claim the same record and
				// token so the retry can complete deterministically.
				if err := s.db.Model(&model.InstanceOperation{}).Where("id = ?", existing.ID).Updates(map[string]any{"state": model.OperationStateRunning, "guard_token": existing.GuardToken}).Error; err != nil {
					return nil, err
				}
				return s.claimFence(ctx, id, action, callerRef, existing.GuardToken)
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	return s.claimFence(ctx, id, action, callerRef, "")
}

// claimFence takes the in-memory + SQL fence and, when a caller id is
// present, inserts the idempotency record. A concurrent duplicate claim
// loses on the unique index and the fence is rolled back.
func (s *VirtualisService) claimFence(ctx context.Context, id uint, action string, callerRef string, reuseToken string) (*operationGuard, error) {
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
	token := reuseToken
	if token == "" {
		token = newOperationID()
	}
	op := &operationGuard{s: s, id: id, token: token, action: action, callerRef: callerRef}
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
	if callerRef != "" && reuseToken == "" {
		record := model.InstanceOperation{CallerOperationID: callerRef, InstanceID: id, Action: action, GuardToken: op.token, State: model.OperationStateRunning}
		if err := s.db.WithContext(ctx).Create(&record).Error; err != nil {
			// Another request claimed the same id first: give the fence
			// back and report the conflict.
			releaseErr := op.release()
			op.unlockMemory()
			return nil, errors.Join(Conflict("operation id %q is already in use", callerRef), releaseErr)
		}
	}
	if err := op.phase("start", "operation started", nil); err != nil {
		releaseErr := op.release()
		op.unlockMemory()
		return nil, errors.Join(err, releaseErr)
	}
	return op, nil
}

// sanitizeCallerRef strips control characters and bounds the length to the
// caller_operation_id column (128, the plugin's validOperationID ceiling).
func sanitizeCallerRef(ref string) string {
	ref = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, ref)
	if len(ref) > 128 {
		ref = ref[:128]
	}
	return ref
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
	row := model.InstanceOperationLog{InstanceID: op.id, OperationID: op.operationID(), CallerRef: op.callerRef, Action: op.action, Stage: stage, Status: status, Message: message}
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
	// A replayed operation never ran: its outcome was already recorded by
	// the original attempt.
	if op.replay {
		return
	}
	row := model.InstanceOperationLog{InstanceID: op.id, OperationID: op.operationID(), CallerRef: op.callerRef, Action: op.action, Stage: "complete", Status: model.OperationSuccess, Message: "operation complete"}
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
	// Complete the idempotency record with the terminal state BEFORE the
	// fence is released: a retry that lands between "remote applied" and
	// "record completed" must still see running-and-fenced (busy), not a
	// fresh claim that re-runs the work.
	if op.callerRef != "" {
		state := model.OperationStateSucceeded
		payload := ""
		if *result != nil {
			state = model.OperationStateFailed
			payload = (*result).Error()
		}
		now := time.Now().UTC()
		if err := op.s.db.WithContext(ctx).Model(&model.InstanceOperation{}).
			Where("caller_operation_id = ? AND instance_id = ?", op.callerRef, op.id).
			Updates(map[string]any{"state": state, "result_payload": payload, "completed_at": &now}).Error; err != nil {
			*result = errors.Join(*result, fmt.Errorf("persist operation record: %w", err))
			op.retain = true
		}
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
