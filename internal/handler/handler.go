package handler

import (
	"log"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/SakuraOpenSource/virtualis/internal/captcha"
	"github.com/SakuraOpenSource/virtualis/internal/runtime"
	"github.com/SakuraOpenSource/virtualis/internal/service"
	"github.com/SakuraOpenSource/virtualis/internal/storage"
)

// Handler groups runtime and shared collaborators.
// Services that need a *gorm.DB are created on demand because the DB
// only exists after installation.
type Handler struct {
	rt           *runtime.Runtime
	install      *service.InstallService
	captchaStore *captcha.Store
	storage      *storage.Store
}

// New creates a Handler backed by rt.
func New(rt *runtime.Runtime) *Handler {
	return &Handler{
		rt:           rt,
		install:      service.NewInstallService(rt),
		captchaStore: captcha.NewStore(),
		storage:      storage.New(rt.DataDir()),
	}
}

// Close releases background resources held by the handler.
// Currently storage and captcha store need no cleanup, but the method
// is kept for symmetry with server lifecycle.
func (h *Handler) Close() {}

func (h *Handler) db() *gorm.DB { return h.rt.DB() }

func (h *Handler) users() *service.UserService { return service.NewUserService(h.db()) }

func (h *Handler) settings() *service.SettingService { return service.NewSettingService(h.db()) }

func (h *Handler) captchaSvc() *service.CaptchaService {
	return service.NewCaptchaService(h.db(), h.captchaStore)
}

func (h *Handler) apiKeys() *service.APIKeyService { return service.NewAPIKeyService(h.db()) }

func (h *Handler) virtualis() *service.VirtualisService {
	return service.NewVirtualisService(h.db(), h.storage)
}

func (h *Handler) agents() *service.AgentService { return service.NewAgentService(h.db()) }

// respond converts service errors into HTTP responses.
// BizError is rendered with its embedded status/code/message,
// any other error becomes 500 without exposing internals.
func respond(c *gin.Context, data any, err error) {
	if err == nil {
		OK(c, data)
		return
	}
	if be, ok := service.AsError(err); ok {
		Fail(c, be.Status, be.Code, be.Message)
		return
	}
	log.Printf("internal error: %v", err)
	Internal(c, "internal server error")
}
