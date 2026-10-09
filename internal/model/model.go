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
	// PasswordChangedAt records the most recent password change (NULL = never
	// changed). RequireAuth compares it against the JWT iat so changing the
	// password immediately kicks every session issued before it, including
	// tokens the in-memory logout revocation list cannot see. The pointer
	// keeps existing rows NULL so upgrades never force a global re-login.
	PasswordChangedAt *time.Time `json:"-"`
	// SessionVersion is the monotonic session-invalidation counter: every
	// password change (and CLI reset) increments it atomically with the new
	// hash, and tokens carry the version they were issued under. RequireAuth
	// demands exact equality. A timestamp alone cannot invalidate a session
	// issued within the same second as the change (JWT iat has second
	// precision), so the counter — not the clock — is the authority. It
	// defaults to 0, and tokens issued by pre-upgrade binaries carry no
	// version claim; those are only accepted while the account is still at
	// version 0, so the first credential rotation retires them too.
	SessionVersion int64 `gorm:"not null;default:0" json:"-"`
}

// TouchPassword records one password change; shared by install, change and
// CLI reset paths so every credential rotation invalidates older sessions.
func (u *User) TouchPassword() {
	now := time.Now().UTC()
	u.PasswordChangedAt = &now
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
		&NATMapping{},
		&InstanceOperationLog{},
		&IPPool{},
		&IPPoolEntry{},
		&VPC{},
		&FirewallRule{},
		&SecurityGroup{},
		&SecurityGroupRule{},
		&InstanceSecurityGroup{},
		&Snapshot{},
		&Backup{},
		&Migration{},
		&RevokedToken{},
		&InstanceOperation{},
	}
}

// RevokedToken is the durable session revocation record backing logout
// (P-AUTH-1). Rows outlive their token's natural expiry only until the next
// sweep. All timestamps are stored and compared in UTC: a local-time
// comparison on a UTC+offset host would resurrect revoked sessions hours
// before their expiry.
type RevokedToken struct {
	JTI      string    `gorm:"primaryKey;size:64" json:"jti"`
	ExpiresAt time.Time `gorm:"index;not null" json:"expires_at"`
}
