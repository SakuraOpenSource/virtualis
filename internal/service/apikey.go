package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/SakuraOpenSource/virtualis/internal/model"
)

const (
	apiKeyPrefix     = "lvs_"
	apiKeyBytes      = 16
	lastUsedThrottle = time.Minute
)

// APIKeyService manages the single site-wide API key. The user id is retained
// only as the owner required by older database schemas; it is not a scope.
type APIKeyService struct {
	db *gorm.DB
}

func NewAPIKeyService(db *gorm.DB) *APIKeyService { return &APIKeyService{db: db} }

// APIKeyCreateRequest is the caller's requested key shape. Scopes and expiry
// are honored as requested (validated against the supported scope set); an
// omitted scopes list falls back to all scopes for wire compatibility with
// older clients that expect a full-permission site key.
type APIKeyCreateRequest struct {
	Name      string   `json:"name"`
	Scopes    []string `json:"scopes"`
	ExpiresIn int      `json:"expires_in_days"`
}

type APIKeyCreated struct {
	Key    *model.APIKey `json:"key"`
	Secret string        `json:"secret"`
}

// requestedScopes validates the requested scope list: it must be non-empty
// and a subset of the supported scopes. Silently widening a requested
// credential (the old behavior) would turn a leaked read-only key into a
// purge-capable one, so unsupported scopes are rejected with 400 instead.
func requestedScopes(req APIKeyCreateRequest) (model.ScopeList, error) {
	if len(req.Scopes) == 0 {
		return model.ScopeList(model.AllScopes()), nil
	}
	out := make(model.ScopeList, 0, len(req.Scopes))
	seen := map[string]bool{}
	for _, scope := range req.Scopes {
		if !model.ValidScope(scope) {
			return nil, BadRequest("unsupported scope %q", scope)
		}
		if seen[scope] {
			continue
		}
		seen[scope] = true
		out = append(out, scope)
	}
	if len(out) == 0 {
		return nil, BadRequest("scopes must not be empty")
	}
	return out, nil
}

// requestedExpiry converts the requested validity in days to an absolute
// expiry. Zero (the default) means the key never expires.
func requestedExpiry(req APIKeyCreateRequest) *time.Time {
	if req.ExpiresIn <= 0 {
		return nil
	}
	at := time.Now().UTC().AddDate(0, 0, req.ExpiresIn)
	return &at
}

// Create creates the only active key, or rotates a previously revoked row,
// honoring the requested scopes and expiry.
func (s *APIKeyService) Create(userID uint, req APIKeyCreateRequest) (*APIKeyCreated, error) {
	scopes, err := requestedScopes(req)
	if err != nil {
		return nil, err
	}
	secret, err := GenerateSecret()
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Virtualis Site Key"
	}
	if len(name) > 64 {
		return nil, BadRequest("key name too long")
	}
	prefixLen := len(apiKeyPrefix) + 8
	key := model.APIKey{
		UserID:    userID,
		Name:      name,
		Prefix:    secret[:prefixLen],
		KeyHash:   HashAPIKey(secret),
		Scopes:    scopes,
		Status:    model.APIKeyActive,
		ExpiresAt: requestedExpiry(req),
	}
	var result *model.APIKey
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var keys []model.APIKey
		if err := tx.Order("id DESC").Find(&keys).Error; err != nil {
			return err
		}
		for _, existing := range keys {
			if existing.Status == model.APIKeyActive {
				return Conflict("站点 API 密钥已存在，请先吊销后再生成")
			}
			break
		}
		if len(keys) > 0 {
			// Reuse the one historical row so the database never accumulates
			// multiple site credentials.
			existing := keys[0]
			if err := tx.Model(&existing).Updates(map[string]any{
				"user_id":      key.UserID,
				"name":         key.Name,
				"prefix":       key.Prefix,
				"key_hash":     key.KeyHash,
				"scopes":       key.Scopes,
				"status":       key.Status,
				"expires_at":   key.ExpiresAt,
				"last_used_at": nil,
			}).Error; err != nil {
				return err
			}
			key.ID = existing.ID
			key.CreatedAt = existing.CreatedAt
			result = &key
			// Old installations may have extra rows. Revoke them so old
			// secrets cannot continue to work.
			for _, extra := range keys[1:] {
				if err := tx.Model(&model.APIKey{}).Where("id = ?", extra.ID).Update("status", model.APIKeyRevoked).Error; err != nil {
					return err
				}
			}
			return nil
		}
		if err := tx.Create(&key).Error; err != nil {
			return err
		}
		result = &key
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &APIKeyCreated{Key: result, Secret: secret}, nil
}

// List is a pure read: it returns the newest row without elevating its scopes
// or revoking other rows. Legacy normalization (scope widening, cleanup of
// stray active rows) happens only on explicit admin actions (Create), never
// as a side effect of a GET — a read must not change credential semantics.
func (s *APIKeyService) List(userID uint) ([]model.APIKey, error) {
	var keys []model.APIKey
	if err := s.db.Order("id DESC").Find(&keys).Error; err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return []model.APIKey{}, nil
	}
	return []model.APIKey{keys[0]}, nil
}

func (s *APIKeyService) Revoke(id, _ uint) error {
	result := s.db.Model(&model.APIKey{}).Where("id = ? AND status = ?", id, model.APIKeyActive).Update("status", model.APIKeyRevoked)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		var count int64
		if err := s.db.Model(&model.APIKey{}).Where("id = ?", id).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return NotFound("key not found")
		}
		return Conflict("key already revoked")
	}
	return nil
}

func (s *APIKeyService) Authenticate(secret string) (*model.APIKey, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" || !strings.HasPrefix(secret, apiKeyPrefix) {
		return nil, Unauthorized("invalid api key")
	}
	var key model.APIKey
	if err := s.db.Where("key_hash = ?", HashAPIKey(secret)).First(&key).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, Unauthorized("invalid api key")
		}
		return nil, err
	}
	if !key.Usable(time.Now().UTC()) {
		return nil, Unauthorized("api key revoked or expired")
	}
	if len(key.Scopes) == 0 {
		return nil, Unauthorized("invalid api key scopes")
	}
	for _, scope := range key.Scopes {
		if !model.ValidScope(scope) {
			return nil, Unauthorized("invalid api key scopes")
		}
	}
	return &key, nil
}

func (s *APIKeyService) TouchLastUsed(key *model.APIKey) {
	now := time.Now().UTC()
	if key.LastUsedAt != nil && now.Sub(*key.LastUsedAt) < lastUsedThrottle {
		return
	}
	_ = s.db.Model(&model.APIKey{}).Where("id = ?", key.ID).UpdateColumn("last_used_at", now).Error
	key.LastUsedAt = &now
}

func HashAPIKey(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func GenerateSecret() (string, error) {
	buf := make([]byte, apiKeyBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return apiKeyPrefix + hex.EncodeToString(buf), nil
}
