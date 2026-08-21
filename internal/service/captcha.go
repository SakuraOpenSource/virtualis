package service

import (
	"strings"

	"gorm.io/gorm"

	"github.com/SakuraOpenSource/virtualis/internal/captcha"
)

const (
	CaptchaSceneLogin    = "login"
	CaptchaSceneRegister = "register"
)

// CaptchaStore is the minimal interface needed from captcha.Store.
type CaptchaStore interface {
	Generate() (*captcha.Challenge, error)
	Verify(id, answer string) bool
}

// CaptchaService wraps captcha store with setting-aware verify.
type CaptchaService struct {
	settings *SettingService
	store    CaptchaStore
}

// NewCaptchaService creates a CaptchaService sharing the process-wide store.
func NewCaptchaService(db *gorm.DB, store CaptchaStore) *CaptchaService {
	return &CaptchaService{settings: NewSettingService(db), store: store}
}

// Issue generates a new challenge according to current settings.
// Math captcha has no charset/length config – we just delegate.
func (s *CaptchaService) Issue() (*captcha.Challenge, error) {
	return s.store.Generate()
}

// Verify checks captcha if enabled for the given scene.
func (s *CaptchaService) Verify(scene, id, answer string) error {
	cfg := s.settings.Captcha()
	enabled := cfg.RegisterEnabled
	if scene == CaptchaSceneLogin {
		enabled = cfg.LoginEnabled
	}
	if !enabled {
		return nil
	}
	if strings.TrimSpace(answer) == "" {
		return BadRequest("captcha required")
	}
	if !s.store.Verify(id, answer) {
		return BadRequest("invalid or expired captcha")
	}
	return nil
}
