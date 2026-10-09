package model

import "time"

const (
	AgentStatusPending = "pending"
	AgentStatusOnline  = "online"
	AgentStatusOffline = "offline"
)

// Agent is a registered machine managed by the master.
// Drivers and Endpoint are reported by the agent itself; the master never
// executes a virtualization command for an instance assigned to an agent.
type Agent struct {
	Base
	Name        string `gorm:"uniqueIndex;size:64;not null" json:"name"`
	DisplayName string `gorm:"size:128" json:"display_name"`
	// The plaintext token is intentionally NOT a column: it is returned once
	// at create/rotate and cached in process memory from heartbeats. Only
	// TokenHash is persisted so database backups never contain node control
	// credentials. Migrations blank out any historical plaintext values.
	TokenHash  string     `gorm:"size:64;not null" json:"-"`
	Status     string     `gorm:"size:16;not null;default:pending" json:"status"`
	IP         string     `gorm:"size:64" json:"ip"`
	Endpoint   string     `gorm:"size:255" json:"endpoint"`
	Driver     string     `gorm:"size:16" json:"driver"`
	Drivers    StringList `gorm:"type:text" json:"drivers"`
	OS         string     `gorm:"size:32" json:"os"`
	Arch       string     `gorm:"size:32" json:"arch"`
	Version    string     `gorm:"size:32" json:"version"`
	LastSeenAt *time.Time `json:"last_seen_at"`
}

func (a Agent) IsOnline() bool { return a.Status == AgentStatusOnline }
