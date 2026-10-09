package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// Token lifetime. Virtualis sessions are all administrator sessions in
// practice, so the shorter AdminTokenTTL below is what applies on login; the
// 7d constant is kept as the ceiling for non-admin roles.
const TokenTTL = 7 * 24 * time.Hour

// AdminTokenTTL is the session lifetime for administrator accounts,
// deliberately shorter than a normal session (same policy as levis): every
// Virtualis account can control compute nodes, so a leaked admin credential
// is far more damaging and the exposure window must stay small. Re-login
// mid-operation is an acceptable cost.
const AdminTokenTTL = 12 * time.Hour

// Cookie and header names.
const (
	CookieToken = "virtualis_token"
	CookieCSRF  = "virtualis_csrf"
	HeaderCSRF  = "X-CSRF-Token"
)

// ErrInvalidToken is returned when token is missing or invalid.
var ErrInvalidToken = errors.New("invalid or expired token")

// Claims holds JWT payload.
type Claims struct {
	jwt.RegisteredClaims
	UserID uint   `json:"uid"`
	Role   string `json:"role"`
	// SessionVersion mirrors users.session_version at issue time. Absent on
	// tokens issued before the field existed (decoded as 0); RequireAuth
	// rejects such legacy tokens once the account version is nonzero, so a
	// password change retires pre-upgrade sessions as well.
	SessionVersion int64 `json:"sess_ver,omitempty"`
}

// DefaultPasswordCost is bcrypt cost for production.
const DefaultPasswordCost = 12

var currentCost = DefaultPasswordCost

// SetPasswordCost overrides bcrypt cost for tests.
func SetPasswordCost(c int) func() {
	if c < bcrypt.MinCost {
		c = bcrypt.MinCost
	}
	prev := currentCost
	currentCost = c
	return func() { currentCost = prev }
}

// HashPassword hashes a plaintext password.
func HashPassword(pw string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), currentCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(h), nil
}

// CheckPassword verifies password against hash.
func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// GenerateToken signs a JWT with the role-appropriate TTL: administrator
// sessions get the shorter AdminTokenTTL (see its comment). sessionVersion
// is the account's current session_version counter; it must be read from
// the database row in the same request that authenticates the user so the
// token is born valid under the current version.
func GenerateToken(secret string, uid uint, role string, sessionVersion int64) (string, time.Time, error) {
	ttl := TokenTTL
	if role == "admin" {
		ttl = AdminTokenTTL
	}
	return GenerateTokenWithTTL(secret, uid, role, ttl, sessionVersion)
}

// GenerateTokenWithTTL signs a JWT with an explicit lifetime. The jti is what
// makes precise logout revocation possible: it is the only handle to identify
// one specific stateless token.
func GenerateTokenWithTTL(secret string, uid uint, role string, ttl time.Duration, sessionVersion int64) (string, time.Time, error) {
	expires := time.Now().Add(ttl)
	jti, err := randomHex(16)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("generate token id: %w", err)
	}
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        jti,
			Subject:   fmt.Sprint(uid),
			ExpiresAt: jwt.NewNumericDate(expires),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
		UserID:         uid,
		Role:           role,
		SessionVersion: sessionVersion,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := tok.SignedString([]byte(secret))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign token: %w", err)
	}
	return s, expires, nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ParseToken validates and parses a JWT.
func ParseToken(secret, token string) (*Claims, error) {
	c := &Claims{}
	parsed, err := jwt.ParseWithClaims(token, c, func(t *jwt.Token) (any, error) {
		return []byte(secret), nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil || !parsed.Valid {
		return nil, ErrInvalidToken
	}
	if c.UserID == 0 {
		return nil, ErrInvalidToken
	}
	return c, nil
}

// GenerateCSRFToken creates a random CSRF token.
func GenerateCSRFToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate csrf: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// SecureCompare does constant-time string comparison.
func SecureCompare(a, b string) bool {
	if len(a) == 0 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
