package model

import "time"

// Base holds common primary key and timestamps.
type Base struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Setting is a simple key-value store for site configuration.
type Setting struct {
	Key   string `gorm:"primaryKey;size:64" json:"key"`
	Value string `gorm:"type:text" json:"value"`
}

// User role constants. Virtualis only has admin accounts.
const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

const (
	SettingSiteName        = "site_name"
	SettingSiteDescription = "site_description"

	SettingCaptchaLogin    = "captcha_login"
	SettingCaptchaRegister = "captcha_register"
	SettingCaptchaCharset  = "captcha_charset"
	SettingCaptchaLength   = "captcha_length"

	SettingDefaultDriver  = "virtualis_default_driver"
	SettingDefaultCPU     = "virtualis_default_cpu"
	SettingDefaultMemory  = "virtualis_default_memory"
	SettingDefaultDisk    = "virtualis_default_disk"
	SettingDefaultArch    = "virtualis_default_arch"
	SettingAllowReinstall = "virtualis_allow_reinstall"
	SettingAutoRefreshSec = "virtualis_auto_refresh_sec"
)

// User status constants.
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
)

// User represents an admin account.
type User struct {
	Base
	Username     string `gorm:"uniqueIndex;size:64;not null" json:"username"`
	Email        string `gorm:"uniqueIndex;size:255;not null" json:"email"`
	PasswordHash string `gorm:"size:255;not null" json:"-"`
	Role         string `gorm:"size:16;not null;default:admin" json:"role"`
	Status       string `gorm:"size:16;not null;default:active" json:"status"`
}

// IsAdmin reports whether the user has admin role.
func (u User) IsAdmin() bool { return u.Role == RoleAdmin }

// AllModels returns every model that needs migration.
func AllModels() []any {
	return []any{
		&Setting{},
		&User{},
		&APIKey{},
		&Instance{},
		&Image{},
		&Agent{},
	}
}
