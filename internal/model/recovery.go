package model

// Recovery records are independent of network/pool ownership. Only permanent
// deletion removes them; archives are never served using an absolute path.
type Snapshot struct {
	Base
	InstanceID uint   `gorm:"not null;uniqueIndex:idx_instance_snapshot,priority:1" json:"instance_id"`
	AgentID    uint   `gorm:"index;not null" json:"agent_id"`
	Name       string `gorm:"size:64;not null;uniqueIndex:idx_instance_snapshot,priority:2" json:"name"`
	Remark     string `gorm:"size:255" json:"remark"`
	SizeBytes  int64  `json:"size_bytes"`
	Status     string `gorm:"size:16;not null" json:"status"`
	Error      string `gorm:"type:text" json:"error,omitempty"`
	ConfigJSON string `gorm:"type:text" json:"-"`
}

type Backup struct {
	Base
	InstanceID uint         `gorm:"index;not null" json:"instance_id"`
	AgentID    uint         `gorm:"index" json:"agent_id"`
	Driver     string       `gorm:"size:16;not null" json:"driver"`
	Name       string       `gorm:"size:64;not null" json:"name"`
	Remark     string       `gorm:"size:255" json:"remark"`
	Format     string       `gorm:"size:16;not null" json:"format"`
	FilePath   string       `gorm:"size:255" json:"-"`
	SizeBytes  int64        `json:"size_bytes"`
	Checksum   string       `gorm:"size:64" json:"checksum"`
	Status     string       `gorm:"size:16;not null" json:"status"`
	Error      string       `gorm:"type:text" json:"error,omitempty"`
	ConfigJSON string       `gorm:"type:text" json:"-"`
	Spec       InstanceSpec `gorm:"type:text;serializer:json" json:"-"`
	Snapshots  []Snapshot   `gorm:"type:text;serializer:json" json:"-"`
}
