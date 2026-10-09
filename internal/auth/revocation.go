package auth

import (
	"sync"
	"time"
)

// revocationSweepInterval is how often expired revocation entries are swept.
const revocationSweepInterval = 10 * time.Minute

// RevocationList is the process-local session revocation registry used by
// logout.
//
// JWTs are stateless: dropping the cookie only affects the browser that still
// has it. A copied token keeps working until natural expiry unless the server
// refuses it, so logout records the token's jti as "explicitly invalidated"
// and RequireAuth consults this list on every request (one map read).
//
// This file is intentionally a close port of levis internal/auth/revocation.go:
// the two backends share the same single-process deployment model and session
// semantics, and keeping the implementations aligned avoids cross-repo drift
// in logout guarantees. Known accepted boundary (same as levis): the list is
// memory-only, so a restart empties it while tokens stay valid. The
// password-change check in RequireAuth (password_changed_at vs JWT iat) covers
// the higher-risk "credential may have been stolen" scenario durably.
type RevocationList struct {
	mu       sync.Mutex
	revoked  map[string]time.Time // key = jti, value = the token's natural expiry
	stopOnce sync.Once
	stop     chan struct{}
}

// NewRevocationList builds a revocation list and starts background sweeping.
func NewRevocationList() *RevocationList {
	r := &RevocationList{
		revoked: make(map[string]time.Time),
		stop:    make(chan struct{}),
	}
	go r.sweepLoop()
	return r
}

// Close stops the background sweeper. Safe to call repeatedly.
func (r *RevocationList) Close() {
	r.stopOnce.Do(func() { close(r.stop) })
}

// Revoke blocks the given jti until its natural expiry.
//
// Entries past expiry are meaningless (the token itself is invalid by then)
// and get swept; passing an earlier expiry never un-revokes — only extends.
func (r *RevocationList) Revoke(jti string, expiry time.Time) {
	if jti == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if until, ok := r.revoked[jti]; !ok || expiry.After(until) {
		r.revoked[jti] = expiry
	}
}

// IsRevoked reports whether the jti has been revoked.
func (r *RevocationList) IsRevoked(jti string) bool {
	if jti == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.revoked[jti]
	return ok
}

// sweepLoop periodically drops entries whose tokens have naturally expired so
// the map cannot grow without bound.
func (r *RevocationList) sweepLoop() {
	ticker := time.NewTicker(revocationSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.stop:
			return
		case now := <-ticker.C:
			r.mu.Lock()
			for jti, expiry := range r.revoked {
				if now.After(expiry) {
					delete(r.revoked, jti)
				}
			}
			r.mu.Unlock()
		}
	}
}
