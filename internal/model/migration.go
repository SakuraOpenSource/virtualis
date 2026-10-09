package model

// Migration retains both stopped runtime identities and the transport archive
// across an uncertain database switch or source cleanup. Never expire its fence.
type Migration struct {
	Base
	InstanceID        uint   `gorm:"index;not null" json:"instance_id"`
	OperationID       string `gorm:"uniqueIndex;size:64;not null" json:"operation_id"`
	SourceAgentID     uint   `json:"source_agent_id"`
	TargetAgentID     uint   `json:"target_agent_id"`
	TargetVPCID       *uint  `gorm:"index" json:"target_vpc_id"`
	SourceVPCID       *uint  `gorm:"index" json:"source_vpc_id,omitempty"`
	TargetPoolEntryID *uint  `json:"target_pool_entry_id"`
	Stage             string `gorm:"size:32;index" json:"stage"`
	SourceJSON        string `gorm:"type:text" json:"-"`
	TargetJSON        string `gorm:"type:text" json:"-"`
	ArchivePath       string `gorm:"size:255" json:"-"`
	Checksum          string `gorm:"size:64" json:"checksum"`
	SizeBytes         int64  `json:"size_bytes"`
	Error             string `gorm:"type:text" json:"error,omitempty"`
}
