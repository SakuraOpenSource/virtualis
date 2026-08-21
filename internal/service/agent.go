package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"gorm.io/gorm"

	"github.com/SakuraOpenSource/virtualis/internal/model"
)

type AgentService struct {
	db *gorm.DB
}

func NewAgentService(db *gorm.DB) *AgentService { return &AgentService{db: db} }

func (s *AgentService) List() ([]model.Agent, error) {
	var items []model.Agent
	if err := s.db.Order("id DESC").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

func (s *AgentService) Create(name, displayName string) (*model.Agent, string, error) {
	if name == "" {
		return nil, "", ErrBadRequest("节点名称不能为空")
	}
	// 生成一次性 token
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", err
	}
	token := hex.EncodeToString(raw)
	hash := sha256.Sum256([]byte(token))
	agent := model.Agent{
		Name:        name,
		DisplayName: displayName,
		TokenHash:   hex.EncodeToString(hash[:]),
		Status:      model.AgentStatusPending,
	}
	if err := s.db.Create(&agent).Error; err != nil {
		return nil, "", err
	}
	return &agent, token, nil
}

func (s *AgentService) Delete(id uint) error {
	return s.db.Delete(&model.Agent{}, id).Error
}

func (s *AgentService) Authenticate(token string) (*model.Agent, error) {
	if token == "" {
		return nil, ErrUnauthorized("token 为空")
	}
	hash := sha256.Sum256([]byte(token))
	h := hex.EncodeToString(hash[:])
	var a model.Agent
	if err := s.db.First(&a, "token_hash = ?", h).Error; err != nil {
		return nil, ErrUnauthorized("token 无效")
	}
	return &a, nil
}

func (s *AgentService) Heartbeat(a *model.Agent, ip, driver, version string) error {
	now := time.Now()
	return s.db.Model(a).Updates(map[string]any{
		"status":       model.AgentStatusOnline,
		"ip":           ip,
		"driver":       driver,
		"version":      version,
		"last_seen_at": now,
	}).Error
}

func (s *AgentService) MarkOfflineIfStale(threshold time.Duration) {
	var agents []model.Agent
	_ = s.db.Find(&agents).Error
	cutoff := time.Now().Add(-threshold)
	for _, a := range agents {
		if a.LastSeenAt != nil && a.LastSeenAt.Before(cutoff) && a.Status == model.AgentStatusOnline {
			_ = s.db.Model(&a).Update("status", model.AgentStatusOffline).Error
		}
	}
}
