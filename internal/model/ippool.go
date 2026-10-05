package model

import "time"

// IP pool entry statuses.
const (
	IPPoolStatusFree     = "free"
	IPPoolStatusAssigned = "assigned"
	IPPoolStatusDisabled = "disabled"
)

// DefaultIPPoolPrefix 是地址池未配置前缀时的默认掩码位宽。
const DefaultIPPoolPrefix = 24

// ValidIPPoolStatus reports whether s is a supported entry status.
func ValidIPPoolStatus(s string) bool {
	switch s {
	case IPPoolStatusFree, IPPoolStatusAssigned, IPPoolStatusDisabled:
		return true
	}
	return false
}

// IPPool stores the network defaults shared by every address of one agent's
// dedicated-IP pool. The row is created lazily on the first save; a missing
// row is equivalent to "no defaults configured".
type IPPool struct {
	Base
	AgentID uint `gorm:"uniqueIndex;not null" json:"agent_id"`
	// Gateway is the default gateway applied to instances that pick a pool
	// address; entries may override it individually.
	Gateway string `gorm:"size:64" json:"gateway"`
	// Prefix is the default subnet prefix (e.g. 24); it is joined with the
	// entry IP to build the instance NIC CIDR.
	Prefix int `gorm:"not null;default:0" json:"prefix"`
	// DNS servers applied to instances that pick a pool address.
	DNS StringList `gorm:"type:text" json:"dns"`
	// Interface is the host NIC/bridge dedicated instances attach to;
	// empty means the agent auto-selects one.
	Interface string `gorm:"size:64" json:"interface"`
	Note      string `gorm:"size:255" json:"note"`
}

// IPPoolEntry is one allocatable address inside an agent's dedicated-IP pool.
type IPPoolEntry struct {
	Base
	AgentID uint   `gorm:"index;not null;uniqueIndex:idx_ip_pool_entry,priority:1" json:"agent_id"`
	IP      string `gorm:"size:64;not null;uniqueIndex:idx_ip_pool_entry,priority:2" json:"ip"`
	// Gateway overrides the pool default for this address when non-empty.
	Gateway string `gorm:"size:64" json:"gateway"`
	// Prefix overrides the pool default for this address when > 0.
	Prefix int `gorm:"not null;default:0" json:"prefix"`
	// Status is one of free / assigned / disabled.
	Status string `gorm:"size:16;not null;default:free" json:"status"`
	Note   string `gorm:"size:255" json:"note"`
	// InstanceID is set while the address is assigned to an instance.
	InstanceID *uint      `gorm:"index" json:"instance_id"`
	AssignedAt *time.Time `json:"assigned_at,omitempty"`
}
