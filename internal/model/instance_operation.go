package model

// Instance operation actions.
const (
	OperationCreate           = "create"
	OperationPower            = "power"
	OperationConfigureNetwork = "configure_network"
	OperationStatus           = "status"
	OperationDelete           = "delete"
)

// Instance operation log statuses.
const (
	OperationRunning = "running"
	OperationSuccess = "success"
	OperationFailed  = "failed"
)

// InstanceOperationLog is an append-only, administrator-visible lifecycle
// record. Error and message are deliberately free of credentials/passwords.
type InstanceOperationLog struct {
	Base
	InstanceID  uint   `gorm:"index:idx_instance_operation_time,priority:1;not null" json:"instance_id"`
	OperationID string `gorm:"index;size:64;not null" json:"operation_id"`
	Action      string `gorm:"size:32;not null" json:"action"`
	Stage       string `gorm:"size:32;not null" json:"stage"`
	Status      string `gorm:"size:16;not null" json:"status"`
	Message     string `gorm:"type:text" json:"message"`
	Error       string `gorm:"type:text" json:"error,omitempty"`
}
