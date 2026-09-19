package service

import (
	"strings"

	"gorm.io/gorm"

	"github.com/SakuraOpenSource/virtualis/internal/captcha"
)

// CaptchaSceneLogin 是唯一的验证码场景：本系统为单管理员部署，没有注册
// 入口，「注册验证码」是从 Levis 抄来的死设置，已删除。
const CaptchaSceneLogin = "login"

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
	if scene != CaptchaSceneLogin || !cfg.LoginEnabled {
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
