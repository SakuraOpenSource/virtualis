package runtime

import (
	"sync"

	"gorm.io/gorm"

	"github.com/SakuraOpenSource/virtualis/internal/config"
)

// Runtime holds mutable global state protected by a mutex.
type Runtime struct {
	mu      sync.RWMutex
	dataDir string
	cfg     *config.Config
	db      *gorm.DB
}

// New creates a runtime in not-installed state.
func New(dir string) *Runtime {
	return &Runtime{dataDir: dir}
}

// DataDir returns the data directory.
func (r *Runtime) DataDir() string { return r.dataDir }

// Installed reports whether DB has been activated.
func (r *Runtime) Installed() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.db != nil
}

// DB returns the gorm handle or nil.
func (r *Runtime) DB() *gorm.DB {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.db
}

// Config returns current config or nil.
func (r *Runtime) Config() *config.Config {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg
}

// JWTSecret returns jwt secret or empty.
func (r *Runtime) JWTSecret() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.cfg == nil {
		return ""
	}
	return r.cfg.JWTSecret
}

// Activate switches runtime to installed state.
func (r *Runtime) Activate(cfg *config.Config, db *gorm.DB) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cfg = cfg
	r.db = db
}
