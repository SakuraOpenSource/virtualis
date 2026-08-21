package captcha

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TTL for a captcha challenge.
const TTL = 5 * time.Minute

const maxEntries = 4096

// Challenge is returned to the client.
type Challenge struct {
	ID        string `json:"id"`
	Question  string `json:"question"`
	ExpiresIn int    `json:"expires_in"`
}

type entry struct {
	answer  string
	expires time.Time
}

// Store holds math captchas in memory.
type Store struct {
	mu    sync.Mutex
	ttl   time.Duration
	items map[string]entry
}

// NewStore creates an empty store.
func NewStore() *Store {
	return &Store{ttl: TTL, items: make(map[string]entry)}
}

// Generate creates a new math captcha and returns challenge.
func (s *Store) Generate() (*Challenge, error) {
	a, err := randInt(1, 20)
	if err != nil {
		return nil, err
	}
	b, err := randInt(1, 20)
	if err != nil {
		return nil, err
	}
	op := "+"
	if a%2 == 0 {
		op = "-"
		// ensure non-negative result
		if b > a {
			a, b = b, a
		}
	}
	var ans int
	var q string
	if op == "+" {
		ans = a + b
		q = fmt.Sprintf("%d + %d = ?", a, b)
	} else {
		ans = a - b
		q = fmt.Sprintf("%d - %d = ?", a, b)
	}

	id, err := newID()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	s.mu.Lock()
	s.evictLocked(now)
	s.items[id] = entry{answer: strconv.Itoa(ans), expires: now.Add(s.ttl)}
	s.mu.Unlock()

	return &Challenge{ID: id, Question: q, ExpiresIn: int(s.ttl.Seconds())}, nil
}

// Verify checks answer and consumes the challenge.
func (s *Store) Verify(id, answer string) bool {
	if id == "" {
		return false
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return false
	}
	s.mu.Lock()
	e, ok := s.items[id]
	delete(s.items, id)
	s.mu.Unlock()
	if !ok || time.Now().After(e.expires) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(answer), []byte(e.answer)) == 1
}

// Len returns number of pending entries.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.items)
}

func (s *Store) evictLocked(now time.Time) {
	for k, v := range s.items {
		if now.After(v.expires) {
			delete(s.items, k)
		}
	}
	if len(s.items) < maxEntries {
		return
	}
	// Remove oldest entry arbitrarily until under limit.
	for k := range s.items {
		delete(s.items, k)
		if len(s.items) < maxEntries {
			break
		}
	}
}

func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func randInt(min, max int) (int, error) {
	if max <= min {
		return min, nil
	}
	// Use crypto/rand for unpredictability.
	b := make([]byte, 1)
	if _, err := rand.Read(b); err != nil {
		return 0, err
	}
	return min + int(b[0])%(max-min+1), nil
}
