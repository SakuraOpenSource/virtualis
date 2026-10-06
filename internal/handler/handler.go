package handler

import (
	"context"
	"log"
	"sync"
	"time"

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
	// virtualis 是跨请求单例：VNC 短票存内存 map，若每次请求新建服务，
	// 签发的票在下一次请求里永远查不到（生产 401 根因）。
	virtSvc   *service.VirtualisService
	virtMu    sync.Mutex
	virtDB    *gorm.DB
	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once
}

// New creates a Handler backed by rt.
func New(rt *runtime.Runtime) *Handler {
	h := &Handler{
		rt:           rt,
		install:      service.NewInstallService(rt),
		captchaStore: captcha.NewStore(),
		storage:      storage.New(rt.DataDir()),
		virtSvc:      service.NewVirtualisService(rt.DB(), storage.New(rt.DataDir())),
		virtDB:       rt.DB(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.done = make(chan struct{})
	go h.trashScheduler(ctx)
	return h
}

// Close releases background resources held by the handler.
// Currently storage and captcha store need no cleanup, but the method
// is kept for symmetry with server lifecycle.
func (h *Handler) Close() { h.closeOnce.Do(func() { h.cancel(); <-h.done }) }

func (h *Handler) db() *gorm.DB { return h.rt.DB() }

func (h *Handler) users() *service.UserService { return service.NewUserService(h.db()) }

func (h *Handler) settings() *service.SettingService { return service.NewSettingService(h.db()) }

func (h *Handler) captchaSvc() *service.CaptchaService {
	return service.NewCaptchaService(h.db(), h.captchaStore)
}

func (h *Handler) apiKeys() *service.APIKeyService { return service.NewAPIKeyService(h.db()) }

func (h *Handler) virtualis() *service.VirtualisService {
	h.virtMu.Lock()
	defer h.virtMu.Unlock()
	db := h.rt.DB()
	if db != h.virtDB {
		h.virtSvc = service.NewVirtualisService(db, h.storage)
		h.virtDB = db
	}
	return h.virtSvc
}

func (h *Handler) cleanupTrash(ctx context.Context) error {
	if !h.rt.Installed() {
		return nil
	}
	result, err := h.virtualis().CleanupTrash(ctx, time.Now().UTC(), 100)
	for _, entry := range result.Failed {
		log.Printf("trash purge %d failed: %s", entry.ID, entry.Reason)
	}
	return err
}
func (h *Handler) trashScheduler(ctx context.Context) {
	defer close(h.done)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := h.cleanupTrash(ctx); err != nil && ctx.Err() == nil {
				log.Printf("trash scheduler: %v", err)
			}
		}
	}
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
