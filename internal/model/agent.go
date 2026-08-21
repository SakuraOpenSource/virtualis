package model

import "time"

const (
	AgentStatusPending = "pending"
	AgentStatusOnline  = "online"
	AgentStatusOffline = "offline"
)

type Agent struct {
	Base
	Name       string     `gorm:"uniqueIndex;size:64;not null" json:"name"`
	DisplayName string    `gorm:"size:128" json:"display_name"`
	TokenHash  string     `gorm:"size:64;not null" json:"-"`
	Status     string     `gorm:"size:16;not null;default:pending" json:"status"`
	IP         string     `gorm:"size:64" json:"ip"`
	Driver     string     `gorm:"size:16" json:"driver"`
	Version    string     `gorm:"size:32" json:"version"`
	LastSeenAt *time.Time `json:"last_seen_at"`
}

func (a Agent) IsOnline() bool { return a.Status == AgentStatusOnline }
