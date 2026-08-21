package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"

	"github.com/SakuraOpenSource/virtualis/internal/model"
)

const apiKeyPrefix = "vls_"
const apiKeyBytes = 16
const maxAPIKeys = 20
const lastUsedThrottle = time.Minute

// APIKeyService manages API keys.
type APIKeyService struct {
	db *gorm.DB
}

// NewAPIKeyService creates an APIKeyService.
func NewAPIKeyService(db *gorm.DB) *APIKeyService {
	return &APIKeyService{db: db}
}

// APIKeyCreateRequest is the input for creating a key.
type APIKeyCreateRequest struct {
	Name      string   `json:"name"`
	Scopes    []string `json:"scopes"`
	ExpiresIn int      `json:"expires_in_days"` // 0 = never expire
}

// APIKeyCreated is the result of Create, containing plaintext secret once.
type APIKeyCreated struct {
	Key    *model.APIKey `json:"key"`
	Secret string        `json:"secret"`
}

// Create generates a new API key.
func (s *APIKeyService) Create(userID uint, req APIKeyCreateRequest) (*APIKeyCreated, error) {
	name := strings.TrimSpace(req.Name)
	if utf8.RuneCountInString(name) == 0 || utf8.RuneCountInString(name) > 64 {
		return nil, BadRequest("name must be 1-64 characters")
	}
	scopes, err := normalizeScopes(req.Scopes)
	if err != nil {
		return nil, err
	}
	if req.ExpiresIn < 0 || req.ExpiresIn > 3650 {
		return nil, BadRequest("expires_in must be 0-3650")
	}
	var active int64
	if err := s.db.Model(&model.APIKey{}).Where("user_id = ? AND status = ?", userID, model.APIKeyActive).Count(&active).Error; err != nil {
		return nil, err
	}
	if active >= maxAPIKeys {
		return nil, Conflict("too many active keys (max %d)", maxAPIKeys)
	}
	secret, err := GenerateSecret()
	if err != nil {
		return nil, err
	}
	var expiresAt *time.Time
	if req.ExpiresIn > 0 {
		t := time.Now().UTC().AddDate(0, 0, req.ExpiresIn)
		expiresAt = &t
	}
	prefixLen := len(apiKeyPrefix) + 8
	if len(secret) < prefixLen {
		prefixLen = len(secret)
	}
	key := model.APIKey{
		UserID:    userID,
		Name:      name,
		Prefix:    secret[:prefixLen],
		KeyHash:   HashAPIKey(secret),
		Scopes:    scopes,
		Status:    model.APIKeyActive,
		ExpiresAt: expiresAt,
	}
	if err := s.db.Create(&key).Error; err != nil {
		return nil, err
	}
	return &APIKeyCreated{Key: &key, Secret: secret}, nil
}

// List returns all keys for a user.
func (s *APIKeyService) List(userID uint) ([]model.APIKey, error) {
	var items []model.APIKey
	if err := s.db.Where("user_id = ?", userID).Order("id DESC").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// Revoke marks a key as revoked.
func (s *APIKeyService) Revoke(id, userID uint) error {
	result := s.db.Model(&model.APIKey{}).Where("id = ? AND user_id = ? AND status = ?", id, userID, model.APIKeyActive).Update("status", model.APIKeyRevoked)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		var count int64
		if err := s.db.Model(&model.APIKey{}).Where("id = ? AND user_id = ?", id, userID).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return NotFound("key not found")
		}
		return Conflict("key already revoked")
	}
	return nil
}

// Authenticate validates a plaintext secret and returns the key.
func (s *APIKeyService) Authenticate(secret string) (*model.APIKey, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" || !strings.HasPrefix(secret, apiKeyPrefix) {
		return nil, Unauthorized("invalid api key")
	}
	var k model.APIKey
	err := s.db.First(&k, "key_hash = ?", HashAPIKey(secret)).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, Unauthorized("invalid api key")
		}
		return nil, err
	}
	if !k.Usable(time.Now().UTC()) {
		return nil, Unauthorized("api key revoked or expired")
	}
	return &k, nil
}

// TouchLastUsed updates last_used_at with throttling.
func (s *APIKeyService) TouchLastUsed(k *model.APIKey) {
	now := time.Now().UTC()
	if k.LastUsedAt != nil && now.Sub(*k.LastUsedAt) < lastUsedThrottle {
		return
	}
	_ = s.db.Model(&model.APIKey{}).Where("id = ?", k.ID).UpdateColumn("last_used_at", now).Error
	k.LastUsedAt = &now
}

// HashAPIKey returns SHA-256 hex digest of secret.
func HashAPIKey(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// GenerateSecret creates a plaintext key with prefix.
func GenerateSecret() (string, error) {
	buf := make([]byte, apiKeyBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return apiKeyPrefix + hex.EncodeToString(buf), nil
}

func normalizeScopes(scopes []string) (model.ScopeList, error) {
	if len(scopes) == 0 {
		return nil, BadRequest("at least one scope required")
	}
	seen := make(map[string]bool, len(scopes))
	for _, sc := range scopes {
		if !model.ValidScope(sc) {
			return nil, BadRequest("unknown scope %q", sc)
		}
		seen[sc] = true
	}
	out := make(model.ScopeList, 0, len(seen))
	for _, s := range model.AllScopes() {
		if seen[s] {
			out = append(out, s)
		}
	}
	return out, nil
}
