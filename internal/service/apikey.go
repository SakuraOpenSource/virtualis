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

// APIKeyCreateRequest is kept wire-compatible, but name/scopes/expiry are no
// longer user-selectable: the site key always has every permission and never
// expires.
type APIKeyCreateRequest struct {
	Name      string   `json:"name"`
	Scopes    []string `json:"scopes"`
	ExpiresIn int      `json:"expires_in_days"`
}

type APIKeyCreated struct {
	Key    *model.APIKey `json:"key"`
	Secret string        `json:"secret"`
}

// Create creates the only active key, or rotates a previously revoked row.
func (s *APIKeyService) Create(userID uint, _ APIKeyCreateRequest) (*APIKeyCreated, error) {
	secret, err := GenerateSecret()
	if err != nil {
		return nil, err
	}
	prefixLen := len(apiKeyPrefix) + 8
	key := model.APIKey{
		UserID:    userID,
		Name:      "Virtualis Site Key",
		Prefix:    secret[:prefixLen],
		KeyHash:   HashAPIKey(secret),
		Scopes:    model.ScopeList(model.AllScopes()),
		Status:    model.APIKeyActive,
		ExpiresAt: nil,
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
				"expires_at":   nil,
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

// List returns at most one row for the site and normalizes legacy rows.
func (s *APIKeyService) List(userID uint) ([]model.APIKey, error) {
	var keys []model.APIKey
	if err := s.db.Order("id DESC").Find(&keys).Error; err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return []model.APIKey{}, nil
	}
	keep := keys[0]
	if keep.Status == model.APIKeyActive {
		if err := s.db.Model(&model.APIKey{}).Where("id <> ? AND status = ?", keep.ID, model.APIKeyActive).Update("status", model.APIKeyRevoked).Error; err != nil {
			return nil, err
		}
	}
	if keep.Scopes == nil || len(keep.Scopes) != len(model.AllScopes()) {
		keep.Scopes = model.ScopeList(model.AllScopes())
		if err := s.db.Model(&keep).Updates(map[string]any{"scopes": keep.Scopes, "user_id": userID}).Error; err != nil {
			return nil, err
		}
	}
	return []model.APIKey{keep}, nil
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
	if len(key.Scopes) != len(model.AllScopes()) {
		return nil, Unauthorized("invalid site api key")
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
