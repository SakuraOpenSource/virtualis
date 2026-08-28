package service

import (
	"strconv"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/SakuraOpenSource/virtualis/internal/model"
)

// Setting keys.
const (
	KeySiteName        = "site_name"
	KeySiteDescription = "site_description"

	KeyCaptchaLogin    = "captcha_login"
	KeyCaptchaRegister = "captcha_register"

	KeyVirtDefaultDriver  = "virtualis_default_driver"
	KeyVirtDefaultCPU     = "virtualis_default_cpu"
	KeyVirtDefaultMemory  = "virtualis_default_memory"
	KeyVirtDefaultDisk    = "virtualis_default_disk"
	KeyVirtDefaultArch    = "virtualis_default_arch"
	KeyVirtAllowReinstall = "virtualis_allow_reinstall"
	KeyVirtAutoRefresh    = "virtualis_auto_refresh"
)

// SiteConfig holds site-level display settings.
type SiteConfig struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// CaptchaConfig holds captcha feature flags.
type CaptchaConfig struct {
	LoginEnabled    bool `json:"login_enabled"`
	RegisterEnabled bool `json:"register_enabled"`
}

// VirtualisSettings holds virtualization defaults.
type VirtualisSettings struct {
	DefaultDriver  string `json:"default_driver"`
	DefaultCPU     int    `json:"default_cpu"`
	DefaultMemory  int    `json:"default_memory"`
	DefaultDisk    int    `json:"default_disk"`
	DefaultArch    string `json:"default_arch"`
	AllowReinstall bool   `json:"allow_reinstall"`
	AutoRefresh    bool   `json:"auto_refresh"`
}

// DefaultVirtualisSettings returns sane defaults.
func DefaultVirtualisSettings() VirtualisSettings {
	return VirtualisSettings{
		DefaultDriver:  "auto",
		DefaultCPU:     2,
		DefaultMemory:  1024,
		DefaultDisk:    20,
		DefaultArch:    "x86_64",
		AllowReinstall: true,
		AutoRefresh:    true,
	}
}

// SettingService reads/writes site configuration.
type SettingService struct {
	db *gorm.DB
}

// NewSettingService creates a SettingService.
func NewSettingService(db *gorm.DB) *SettingService {
	return &SettingService{db: db}
}

// Site returns current site config.
func (s *SettingService) Site() SiteConfig {
	var rows []model.Setting
	_ = s.db.Where(map[string]any{"key": []string{KeySiteName, KeySiteDescription}}).Find(&rows).Error
	out := SiteConfig{Name: "Virtualis", Description: ""}
	for _, r := range rows {
		switch r.Key {
		case KeySiteName:
			if r.Value != "" {
				out.Name = r.Value
			}
		case KeySiteDescription:
			out.Description = r.Value
		}
	}
	return out
}

// SaveSite persists site config.
func (s *SettingService) SaveSite(in SiteConfig) (SiteConfig, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return SiteConfig{}, BadRequest("site name required")
	}
	if len(in.Name) > 64 {
		return SiteConfig{}, BadRequest("site name too long")
	}
	in.Description = strings.TrimSpace(in.Description)
	rows := []model.Setting{
		{Key: KeySiteName, Value: in.Name},
		{Key: KeySiteDescription, Value: in.Description},
	}
	if err := s.upsert(rows); err != nil {
		return SiteConfig{}, err
	}
	return in, nil
}

// Captcha returns captcha config with defaults.
func (s *SettingService) Captcha() CaptchaConfig {
	out := CaptchaConfig{LoginEnabled: false, RegisterEnabled: false}
	var rows []model.Setting
	_ = s.db.Where(map[string]any{"key": []string{KeyCaptchaLogin, KeyCaptchaRegister}}).Find(&rows).Error
	for _, r := range rows {
		switch r.Key {
		case KeyCaptchaLogin:
			out.LoginEnabled = r.Value == "1"
		case KeyCaptchaRegister:
			out.RegisterEnabled = r.Value == "1"
		}
	}
	return out
}

// SaveCaptcha persists captcha config.
func (s *SettingService) SaveCaptcha(in CaptchaConfig) (CaptchaConfig, error) {
	rows := []model.Setting{
		{Key: KeyCaptchaLogin, Value: boolVal(in.LoginEnabled)},
		{Key: KeyCaptchaRegister, Value: boolVal(in.RegisterEnabled)},
	}
	if err := s.upsert(rows); err != nil {
		return CaptchaConfig{}, err
	}
	return in, nil
}

// Virtualis returns virtualization defaults, merging DB values over hardcoded defaults.
func (s *SettingService) Virtualis() VirtualisSettings {
	def := DefaultVirtualisSettings()
	var rows []model.Setting
	keys := []string{
		KeyVirtDefaultDriver, KeyVirtDefaultCPU, KeyVirtDefaultMemory,
		KeyVirtDefaultDisk, KeyVirtDefaultArch, KeyVirtAllowReinstall, KeyVirtAutoRefresh,
	}
	_ = s.db.Where(map[string]any{"key": keys}).Find(&rows).Error
	for _, r := range rows {
		switch r.Key {
		case KeyVirtDefaultDriver:
			if r.Value != "" {
				def.DefaultDriver = r.Value
			}
		case KeyVirtDefaultCPU:
			if n, err := strconv.Atoi(r.Value); err == nil && n > 0 {
				def.DefaultCPU = n
			}
		case KeyVirtDefaultMemory:
			if n, err := strconv.Atoi(r.Value); err == nil && n > 0 {
				def.DefaultMemory = n
			}
		case KeyVirtDefaultDisk:
			if n, err := strconv.Atoi(r.Value); err == nil && n > 0 {
				def.DefaultDisk = n
			}
		case KeyVirtDefaultArch:
			if r.Value != "" {
				def.DefaultArch = r.Value
			}
		case KeyVirtAllowReinstall:
			def.AllowReinstall = r.Value == "1"
		case KeyVirtAutoRefresh:
			def.AutoRefresh = r.Value == "1"
		}
	}
	// Clamp to valid ranges
	if def.DefaultCPU < 1 {
		def.DefaultCPU = 1
	}
	if def.DefaultCPU > 64 {
		def.DefaultCPU = 64
	}
	if def.DefaultMemory < 128 {
		def.DefaultMemory = 128
	}
	if def.DefaultArch == "" {
		def.DefaultArch = "x86_64"
	}
	return def
}

// SaveVirtualis validates and persists virtualization defaults.
func (s *SettingService) SaveVirtualis(in VirtualisSettings) (VirtualisSettings, error) {
	in.DefaultDriver = strings.TrimSpace(strings.ToLower(in.DefaultDriver))
	if in.DefaultDriver != "" && !model.ValidDriver(in.DefaultDriver) {
		return VirtualisSettings{}, BadRequest("invalid driver %q", in.DefaultDriver)
	}
	if in.DefaultDriver == "" {
		in.DefaultDriver = DefaultVirtualisSettings().DefaultDriver
	}
	in.DefaultArch = strings.TrimSpace(in.DefaultArch)
	if in.DefaultArch == "" {
		in.DefaultArch = "x86_64"
	}
	if in.DefaultCPU < 1 || in.DefaultCPU > 64 {
		return VirtualisSettings{}, BadRequest("cpu must be 1-64")
	}
	if in.DefaultMemory < 128 || in.DefaultMemory > 65536 {
		return VirtualisSettings{}, BadRequest("memory must be 128-65536 MB")
	}
	if in.DefaultDisk < 5 || in.DefaultDisk > 2048 {
		return VirtualisSettings{}, BadRequest("disk must be 5-2048 GB")
	}
	// Validate arch limited set
	validArch := map[string]bool{"x86_64": true, "arm64": true, "aarch64": true, "amd64": true}
	if !validArch[in.DefaultArch] && !validArch[strings.ToLower(in.DefaultArch)] {
		// allow any but normalize
	}

	rows := []model.Setting{
		{Key: KeyVirtDefaultDriver, Value: in.DefaultDriver},
		{Key: KeyVirtDefaultCPU, Value: strconv.Itoa(in.DefaultCPU)},
		{Key: KeyVirtDefaultMemory, Value: strconv.Itoa(in.DefaultMemory)},
		{Key: KeyVirtDefaultDisk, Value: strconv.Itoa(in.DefaultDisk)},
		{Key: KeyVirtDefaultArch, Value: in.DefaultArch},
		{Key: KeyVirtAllowReinstall, Value: boolVal(in.AllowReinstall)},
		{Key: KeyVirtAutoRefresh, Value: boolVal(in.AutoRefresh)},
	}
	if err := s.upsert(rows); err != nil {
		return VirtualisSettings{}, err
	}
	return in, nil
}

func (s *SettingService) upsert(rows []model.Setting) error {
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&rows).Error
}

func boolVal(v bool) string {
	if v {
		return "1"
	}
	return "0"
}
