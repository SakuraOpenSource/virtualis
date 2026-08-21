package service

import (
	"path/filepath"
	"strings"

	"gorm.io/gorm"

	"github.com/SakuraOpenSource/virtualis/internal/auth"
	"github.com/SakuraOpenSource/virtualis/internal/config"
	"github.com/SakuraOpenSource/virtualis/internal/database"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"github.com/SakuraOpenSource/virtualis/internal/runtime"
)

// InstallRequest is the payload for initial setup.
type InstallRequest struct {
	Database        config.Database `json:"database"`
	SiteName        string          `json:"site_name"`
	SiteDescription string          `json:"site_description"`
	AdminUsername   string          `json:"admin_username"`
	AdminEmail      string          `json:"admin_email"`
	AdminPassword   string          `json:"admin_password"`
}

// InstallService handles installation and bootstrap.
type InstallService struct {
	rt *runtime.Runtime
}

// NewInstallService creates an InstallService.
func NewInstallService(rt *runtime.Runtime) *InstallService {
	return &InstallService{rt: rt}
}

// TestDatabase checks that database configuration is reachable.
func (s *InstallService) TestDatabase(db config.Database) error {
	s.normalizeDatabase(&db)
	if err := db.Validate(); err != nil {
		return BadRequest("%s", err.Error())
	}
	if err := database.TestConnection(db); err != nil {
		return BadRequest("%s", err.Error())
	}
	return nil
}

// Install performs full installation atomically.
func (s *InstallService) Install(req InstallRequest) error {
	if s.rt.Installed() {
		return Conflict("already installed")
	}
	s.normalizeDatabase(&req.Database)
	req.SiteName = strings.TrimSpace(req.SiteName)
	req.SiteDescription = strings.TrimSpace(req.SiteDescription)
	req.AdminUsername = strings.TrimSpace(req.AdminUsername)

	if req.SiteName == "" {
		return BadRequest("site name required")
	}
	if err := ValidateUsername(req.AdminUsername); err != nil {
		return err
	}
	email, err := ValidateEmail(req.AdminEmail)
	if err != nil {
		return err
	}
	if err := ValidatePassword(req.AdminPassword); err != nil {
		return err
	}
	if err := req.Database.Validate(); err != nil {
		return BadRequest("%s", err.Error())
	}

	db, err := database.Open(req.Database)
	if err != nil {
		return BadRequest("%s", err.Error())
	}
	if err := database.Migrate(db); err != nil {
		return BadRequest("%s", err.Error())
	}
	var existing int64
	if err := db.Model(&model.User{}).Count(&existing).Error; err != nil {
		return err
	}
	if existing > 0 {
		return Conflict("database already contains users")
	}

	hash, err := auth.HashPassword(req.AdminPassword)
	if err != nil {
		return err
	}

	err = db.Transaction(func(tx *gorm.DB) error {
		admin := model.User{
			Username:     req.AdminUsername,
			Email:        email,
			PasswordHash: hash,
			Role:         model.RoleAdmin,
			Status:       model.StatusActive,
		}
		if err := tx.Create(&admin).Error; err != nil {
			return err
		}
		settings := []model.Setting{
			{Key: KeySiteName, Value: req.SiteName},
			{Key: KeySiteDescription, Value: req.SiteDescription},
		}
		// Persist virtualis defaults as well.
		def := DefaultVirtualisSettings()
		settings = append(settings,
			model.Setting{Key: KeyVirtDefaultDriver, Value: def.DefaultDriver},
			model.Setting{Key: KeyVirtDefaultCPU, Value: "2"},
			model.Setting{Key: KeyVirtDefaultMemory, Value: "1024"},
			model.Setting{Key: KeyVirtDefaultDisk, Value: "20"},
			model.Setting{Key: KeyVirtDefaultArch, Value: def.DefaultArch},
			model.Setting{Key: KeyVirtAllowReinstall, Value: boolVal(def.AllowReinstall)},
			model.Setting{Key: KeyVirtAutoRefresh, Value: boolVal(def.AutoRefresh)},
		)
		return tx.Create(&settings).Error
	})
	if err != nil {
		return err
	}

	secret, err := config.GenerateSecret()
	if err != nil {
		return err
	}
	cfg := &config.Config{
		Database:  req.Database,
		JWTSecret: secret,
		Listen:    config.DefaultListen,
	}
	if err := config.Save(s.rt.DataDir(), cfg); err != nil {
		return err
	}
	s.rt.Activate(cfg, db)
	return nil
}

// Bootstrap returns initial data for frontend.
type Bootstrap struct {
	Installed       bool          `json:"installed"`
	SiteName        string        `json:"site_name"`
	SiteDescription string        `json:"site_description"`
	Captcha         CaptchaScenes `json:"captcha"`
}

// CaptchaScenes indicates which forms require captcha.
type CaptchaScenes struct {
	Login    bool   `json:"login"`
	Register bool   `json:"register"`
	Charset  string `json:"charset,omitempty"`
}

// Bootstrap collects installation status and site settings.
func (s *InstallService) Bootstrap() Bootstrap {
	out := Bootstrap{Installed: s.rt.Installed()}
	if !out.Installed {
		return out
	}
	ss := NewSettingService(s.rt.DB())
	site := ss.Site()
	out.SiteName = site.Name
	out.SiteDescription = site.Description
	cap := ss.Captcha()
	out.Captcha = CaptchaScenes{
		Login:    cap.LoginEnabled,
		Register: cap.RegisterEnabled,
	}
	return out
}

func (s *InstallService) normalizeDatabase(db *config.Database) {
	db.Driver = strings.ToLower(strings.TrimSpace(db.Driver))
	db.Host = strings.TrimSpace(db.Host)
	db.User = strings.TrimSpace(db.User)
	db.Name = strings.TrimSpace(db.Name)
	db.Path = strings.TrimSpace(db.Path)

	switch db.Driver {
	case config.DriverSQLite:
		if db.Path == "" {
			db.Path = filepath.Join(s.rt.DataDir(), "virtualis.db")
		}
		db.Host, db.Port, db.User, db.Password, db.Name = "", 0, "", "", ""
	case config.DriverMySQL:
		if db.Port == 0 {
			db.Port = 3306
		}
	case config.DriverPostgres:
		if db.Port == 0 {
			db.Port = 5432
		}
	}
}
