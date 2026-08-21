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

// Token lifetime.
const TokenTTL = 7 * 24 * time.Hour

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

// GenerateToken signs a JWT for the given user.
func GenerateToken(secret string, uid uint, role string) (string, time.Time, error) {
	exp := time.Now().Add(TokenTTL)
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   fmt.Sprint(uid),
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
		UserID: uid,
		Role:   role,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := tok.SignedString([]byte(secret))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign token: %w", err)
	}
	return s, exp, nil
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
