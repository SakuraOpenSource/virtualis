package model

import "time"

// InstanceOperation is the persistent, idempotent record of a caller-driven
// lifecycle operation keyed by the caller's correlation id
// (X-Levis-Operation-ID). It turns the header from a log suffix into a
// durable idempotency key: a retried request with the same id is matched to
// the original operation instead of re-running its destructive RPCs.
//
// Scope: the unique key is (caller_operation_id, instance_id, action). The
// master deployment is single-process per database; the unique index makes
// double-claiming impossible even across replicas or a crash-restart race.
type InstanceOperation struct {
	Base
	// CallerOperationID is the upstream id verbatim. It is stored in its own
	// 128-byte column (the plugin's validOperationID ceiling), never
	// concatenated into operation_id: composing "<token>/<caller>" produced
	// values longer than the varchar(64) log column on PostgreSQL/MySQL,
	// failing log writes AFTER the remote work had already been applied.
	CallerOperationID string `gorm:"uniqueIndex:idx_caller_operation;size:128;not null" json:"caller_operation_id"`
	InstanceID        uint   `gorm:"uniqueIndex:idx_caller_operation;index;not null" json:"instance_id"`
	Action            string `gorm:"uniqueIndex:idx_caller_operation;size:32;not null" json:"action"`
	// GuardToken is the internal fence token that owns the operation; it
	// links the record to busy_operation and the stage logs.
	GuardToken string `gorm:"size:64;not null" json:"guard_token"`
	// State is claim lifecycle: running -> succeeded | failed.
	State string `gorm:"size:16;not null;default:running" json:"state"`
	// ResultPayload is a short machine-readable summary of the completed
	// outcome replayed to idempotent retries (instance JSON or error text).
	ResultPayload string     `gorm:"type:text" json:"result_payload,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
}

// Instance operation record states.
const (
	OperationStateRunning   = "running"
	OperationStateSucceeded = "succeeded"
	OperationStateFailed    = "failed"
)
