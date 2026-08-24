package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
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

func (s *AgentService) Get(id uint) (*model.Agent, error) {
	var agent model.Agent
	if err := s.db.First(&agent, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, NotFound("被控节点不存在")
		}
		return nil, err
	}
	return &agent, nil
}

func (s *AgentService) Create(name, displayName string) (*model.Agent, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, "", ErrBadRequest("节点名称不能为空")
	}
	if len(name) > 64 {
		return nil, "", ErrBadRequest("节点名称不能超过 64 个字符")
	}
	name = strings.TrimSpace(name)
	displayName = strings.TrimSpace(displayName)
	if len(displayName) > 128 {
		return nil, "", ErrBadRequest("节点显示名称不能超过 128 个字符")
	}

	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", err
	}
	token := hex.EncodeToString(raw)
	hash := sha256.Sum256([]byte(token))
	agent := model.Agent{
		Name:        name,
		DisplayName: displayName,
		Token:       token,
		TokenHash:   hex.EncodeToString(hash[:]),
		Status:      model.AgentStatusPending,
	}
	if err := s.db.Create(&agent).Error; err != nil {
		return nil, "", err
	}
	return &agent, token, nil
}

func (s *AgentService) Delete(id uint) error {
	result := s.db.Delete(&model.Agent{}, id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return NotFound("被控节点不存在")
	}
	return nil
}

func (s *AgentService) Authenticate(token string) (*model.Agent, error) {
	if strings.TrimSpace(token) == "" {
		return nil, ErrUnauthorized("token 为空")
	}
	hash := sha256.Sum256([]byte(strings.TrimSpace(token)))
	var agent model.Agent
	if err := s.db.First(&agent, "token_hash = ?", hex.EncodeToString(hash[:])).Error; err != nil {
		return nil, ErrUnauthorized("token 无效")
	}
	return &agent, nil
}

// Heartbeat updates the endpoint and capabilities used by the master for RPC.
func (s *AgentService) Heartbeat(agent *model.Agent, token, ip, endpoint, primaryDriver, osName, arch, version string, drivers []string) error {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if endpoint != "" {
		u, err := url.Parse(endpoint)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return ErrBadRequest("被控 endpoint 必须是 http:// 或 https:// 地址")
		}
	}
	driverList := normalizeDrivers(drivers)
	if primaryDriver == "" && len(driverList) > 0 {
		primaryDriver = driverList[0]
	}
	now := time.Now().UTC()
	updates := map[string]any{
		"status":       model.AgentStatusOnline,
		"ip":           strings.TrimSpace(ip),
		"endpoint":     endpoint,
		"driver":       strings.TrimSpace(primaryDriver),
		"drivers":      driverList,
		"os":           strings.TrimSpace(osName),
		"arch":         strings.TrimSpace(arch),
		"version":      strings.TrimSpace(version),
		"last_seen_at": now,
	}
	// Older agent rows only stored a hash. A successful authenticated heartbeat
	// lets the master upgrade that row so it can call the agent back for RPC.
	if strings.TrimSpace(token) != "" {
		updates["token"] = strings.TrimSpace(token)
	}
	return s.db.Model(agent).Updates(updates).Error
}

func normalizeDrivers(drivers []string) model.StringList {
	seen := make(map[string]bool, len(drivers))
	out := make(model.StringList, 0, len(drivers))
	for _, name := range drivers {
		name = strings.ToLower(strings.TrimSpace(name))
		if !model.ValidDriver(name) || name == model.DriverAuto || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

func (s *AgentService) MarkOfflineIfStale(threshold time.Duration) {
	var agents []model.Agent
	if err := s.db.Find(&agents).Error; err != nil {
		return
	}
	cutoff := time.Now().Add(-threshold)
	for _, agent := range agents {
		if agent.LastSeenAt != nil && agent.LastSeenAt.Before(cutoff) && agent.Status == model.AgentStatusOnline {
			_ = s.db.Model(&agent).Update("status", model.AgentStatusOffline).Error
		}
	}
}

func hasDriver(agent *model.Agent, name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == model.DriverAuto {
		return agent != nil && len(agent.Drivers) > 0
	}
	if agent == nil {
		return false
	}
	for _, driver := range agent.Drivers {
		if driver == name {
			return true
		}
	}
	return false
}
