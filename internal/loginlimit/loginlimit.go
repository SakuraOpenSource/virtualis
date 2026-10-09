// Package loginlimit provides in-process rate limiting for login failures,
// counting per account and per source IP with temporary lockouts to blunt
// online password guessing.
//
// This package is ported from levis internal/loginlimit (cross-repo copy is
// deliberate: both backends deploy as single-process masters facing the same
// admin-login threat model, and the shared tuning — 5 account failures lock
// exponentially from 15 minutes, 50 failures/hour per IP — must not drift
// between them). It is deliberately memory-only: failure counters are
// short-lived defense data, and persisting them would hand attackers a write
// amplification vector (every failed attempt hits disk). A restart clears the
// counters, which also resets attacker progress; legitimate users keep no
// state worth preserving.
package loginlimit

import (
	"strings"
	"sync"
	"time"
)

// Rate limit tuning.
//
// Account dimension uses consecutive failures + exponential lockout: 5
// failures lock 15 minutes, doubling while attacks continue (15->30->60 min,
// capped at 24h); the counter resets after an hour without new failures so
// occasional typos do not accumulate. The IP dimension is a sliding window
// with a relaxed threshold (50/hour) so unrelated users behind shared egress
// (corporate NAT, campus networks) are not collectively punished.
const (
	accountFailLimit = 5
	accountWindow    = time.Hour
	accountLockBase  = 15 * time.Minute
	ipFailLimit      = 50
	ipWindow         = time.Hour
	ipLock           = 15 * time.Minute
	maxLock          = 24 * time.Hour
	recordTTL        = 24 * time.Hour
	sweepInterval    = 10 * time.Minute
	// maxRecords bounds the account record count (memory protection). It is
	// far above normal traffic because the IP limiter already throttles
	// per-source record creation; this is only a defensive ceiling.
	maxRecords = 100_000
)

// accountRecord is the failure state of one account (per entry scope).
type accountRecord struct {
	failures    int
	lockedUntil time.Time
	lastFailure time.Time
}

// sourceRecord is the failure state of one source IP.
type sourceRecord struct {
	failures    []time.Time // failure timestamps inside the sliding window
	lockedUntil time.Time
	lastFailure time.Time
}

// Tracker is the process-wide login limiter. Issue and verify happen in
// different requests, so one instance must be shared by the whole process;
// the Handler owns it and closes it to release the sweeper goroutine.
type Tracker struct {
	now func() time.Time

	mu       sync.Mutex
	accounts map[string]*accountRecord
	sources  map[string]*sourceRecord

	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
}

// New builds a Tracker using the real clock.
func New() *Tracker {
	return NewWithClock(time.Now)
}

// NewWithClock allows tests to inject a fake clock; production always uses New.
func NewWithClock(now func() time.Time) *Tracker {
	t := &Tracker{
		now:      now,
		accounts: make(map[string]*accountRecord),
		sources:  make(map[string]*sourceRecord),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go t.sweepLoop()
	return t
}

// Close stops the sweeper goroutine. Safe to call repeatedly.
func (t *Tracker) Close() {
	t.stopOnce.Do(func() { close(t.stop) })
}

// Check reports whether the account and source IP may currently attempt a
// login. When refused, wait is the remaining lock duration; the message must
// not reveal which dimension is locked, to avoid telling probers which knob
// they hit.
func (t *Tracker) Check(scope, identifier, ip string) (ok bool, wait time.Duration) {
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	wait = time.Duration(0)
	if r := t.accounts[accountKey(scope, identifier)]; r != nil {
		if d := r.lockedUntil.Sub(now); d > wait {
			wait = d
		}
	}
	if s := t.sources[ip]; s != nil {
		if d := s.lockedUntil.Sub(now); d > wait {
			wait = d
		}
	}
	return wait <= 0, wait
}

// RecordFailure records one credential-verification failure and locks at the
// threshold.
//
// Unknown accounts count too: a non-existent identifier always fails, and
// counting it under the same key blocks the "rotate through fake usernames"
// bypass; on the IP dimension such requests are exactly what credential
// stuffing looks like. Failures that never reached credential verification
// (captcha failure, malformed body) must NOT call this — otherwise an attacker
// could lock other people's accounts without ever touching a password.
func (t *Tracker) RecordFailure(scope, identifier, ip string) {
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.recordAccountFailure(scope, identifier, now)
	t.recordSourceFailure(ip, now)
}

// RecordSuccess clears the account's failure counter for this entry scope.
// Source history is kept and expires through the sliding window: one
// successful login must not launder stuffing history on the same IP.
func (t *Tracker) RecordSuccess(scope, identifier string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.accounts, accountKey(scope, identifier))
}

func (t *Tracker) recordAccountFailure(scope, identifier string, now time.Time) {
	key := accountKey(scope, identifier)
	r := t.accounts[key]
	if r == nil {
		if len(t.accounts) >= maxRecords {
			// Ceiling: sweep expired records first, and if still over the
			// limit, refuse to grow the map further.
			t.sweepLocked(now)
			if len(t.accounts) >= maxRecords {
				return
			}
		}
		r = &accountRecord{}
		t.accounts[key] = r
	}
	if now.Sub(r.lastFailure) > accountWindow {
		r.failures = 0
	}
	r.failures++
	r.lastFailure = now
	if r.failures >= accountFailLimit {
		lock := accountLockBase << uint(r.failures-accountFailLimit)
		if lock <= 0 || lock > maxLock {
			lock = maxLock
		}
		if until := now.Add(lock); until.After(r.lockedUntil) {
			r.lockedUntil = until
		}
	}
}

func (t *Tracker) recordSourceFailure(ip string, now time.Time) {
	s := t.sources[ip]
	if s == nil {
		s = &sourceRecord{}
		t.sources[ip] = s
	}
	window := s.failures[:0]
	for _, ts := range s.failures {
		if now.Sub(ts) <= ipWindow {
			window = append(window, ts)
		}
	}
	window = append(window, now)
	// Keep only the most recent ipFailLimit points: no new records arrive
	// during a lockout, so a larger window would only waste memory.
	if len(window) > ipFailLimit {
		window = window[len(window)-ipFailLimit:]
	}
	s.failures = window
	s.lastFailure = now
	if len(s.failures) >= ipFailLimit {
		if until := now.Add(ipLock); until.After(s.lockedUntil) {
			s.lockedUntil = until
		}
	}
}

// sweepLoop periodically drops long-expired records.
func (t *Tracker) sweepLoop() {
	defer close(t.done)
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-t.stop:
			return
		case now := <-ticker.C:
			t.sweepTick(now)
		}
	}
}

// sweepTick runs one sweep pass under the tracker mutex. The ticker branch
// previously called sweepLocked directly, racing unauthenticated login
// writes; Go's concurrent map iteration/write detection then kills the whole
// process with a fatal error that no HTTP recovery can catch — a remote DoS
// of the control plane from mere login traffic. Every sweep entry point
// (ticker and the maxRecords ceiling inside recordAccountFailure) must go
// through a lock-holding path; sweepLocked stays for callers that already
// hold the mutex.
func (t *Tracker) sweepTick(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sweepLocked(now)
}

// sweepLocked drops expired records; the caller must hold the lock.
func (t *Tracker) sweepLocked(now time.Time) {
	for key, r := range t.accounts {
		if now.Sub(r.lastFailure) > recordTTL && now.After(r.lockedUntil) {
			delete(t.accounts, key)
		}
	}
	for key, s := range t.sources {
		if now.Sub(s.lastFailure) > ipWindow && now.After(s.lockedUntil) {
			delete(t.sources, key)
		}
	}
}

// accountKey builds the account-dimension key. The entry scope is included so
// normal and admin entrances count separately; identifiers are lowercased so
// case variants cannot reset the counter (MySQL's default collation matches
// usernames case-insensitively, which would allow one free retry per variant).
func accountKey(scope, identifier string) string {
	return scope + "|" + strings.ToLower(strings.TrimSpace(identifier))
}
