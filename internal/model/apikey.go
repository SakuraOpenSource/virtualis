package model

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

// Scopes for Virtualis open API.
const (
	ScopeInstanceRead  = "instance:read"
	ScopeInstanceWrite = "instance:write"
	ScopeImageRead     = "image:read"
	ScopeImageWrite    = "image:write"
)

// AllScopes returns the supported scope list.
func AllScopes() []string {
	return []string{ScopeInstanceRead, ScopeInstanceWrite, ScopeImageRead, ScopeImageWrite}
}

// ValidScope reports whether s is a known scope.
func ValidScope(s string) bool {
	switch s {
	case ScopeInstanceRead, ScopeInstanceWrite, ScopeImageRead, ScopeImageWrite:
		return true
	}
	return false
}

// API key status values.
const (
	APIKeyActive  = "active"
	APIKeyRevoked = "revoked"
)

// ScopeList is stored as JSON text in a single column.
type ScopeList []string

// Value implements driver.Valuer.
func (s ScopeList) Value() (driver.Value, error) {
	if len(s) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal([]string(s))
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// Scan implements sql.Scanner.
func (s *ScopeList) Scan(src any) error {
	if src == nil {
		*s = nil
		return nil
	}
	var data []byte
	switch v := src.(type) {
	case []byte:
		data = v
	case string:
		data = []byte(v)
	default:
		return fmt.Errorf("unsupported ScopeList type %T", src)
	}
	if len(data) == 0 {
		*s = nil
		return nil
	}
	var out []string
	if err := json.Unmarshal(data, &out); err != nil {
		return fmt.Errorf("invalid ScopeList json: %w", err)
	}
	*s = out
	return nil
}

// Has reports whether list contains scope.
func (s ScopeList) Has(scope string) bool {
	for _, v := range s {
		if v == scope {
			return true
		}
	}
	return false
}

// APIKey is a credential for open API access.
type APIKey struct {
	Base
	UserID     uint       `gorm:"index;not null" json:"user_id"`
	Name       string     `gorm:"size:64;not null" json:"name"`
	Prefix     string     `gorm:"size:16;not null" json:"prefix"`
	KeyHash    string     `gorm:"uniqueIndex;size:64;not null" json:"-"`
	Scopes     ScopeList  `gorm:"type:text" json:"scopes"`
	Status     string     `gorm:"size:16;not null;default:active" json:"status"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

// Usable reports whether the key is active and not expired.
func (k APIKey) Usable(now time.Time) bool {
	if k.Status != APIKeyActive {
		return false
	}
	if k.ExpiresAt != nil && !k.ExpiresAt.After(now) {
		return false
	}
	return true
}
