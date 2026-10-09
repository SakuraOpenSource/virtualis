package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/SakuraOpenSource/virtualis/internal/model"
)

// agentTokenCache holds plaintext agent RPC tokens in process memory only.
//
// SC-03 master side: the agents table must never persist the plaintext token
// (a database backup read would otherwise hand over node control). The token
// is returned exactly once at create/rotate time, and re-populated in memory
// from the agent's authenticated heartbeat (the agent presents
// X-Agent-Token on every 30s register call). After a master restart the cache
// is cold until each online agent's next heartbeat — RPC calls in that window
// fail with a clear "waiting for node heartbeat" error instead of silently
// using a stale secret.
type agentTokenCache struct {
	mu     sync.Mutex
	tokens map[uint]string
}

func (c *agentTokenCache) set(agentID uint, token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tokens == nil {
		c.tokens = make(map[uint]string)
	}
	c.tokens[agentID] = token
}

func (c *agentTokenCache) get(agentID uint) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	tok, ok := c.tokens[agentID]
	return tok, ok && tok != ""
}

func (c *agentTokenCache) drop(agentID uint) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.tokens, agentID)
}

type AgentService struct {
	db    *gorm.DB
	cache *agentTokenCache
}

// NewAgentService creates a service bound to the process-wide token cache.
func NewAgentService(db *gorm.DB) *AgentService {
	return &AgentService{db: db, cache: globalAgentTokenCache()}
}

var (
	agentTokenCacheOnce sync.Once
	agentTokens         *agentTokenCache
)

// globalAgentTokenCache lazily builds the single process-wide cache; the
// master must present one consistent view of agent tokens across services.
func globalAgentTokenCache() *agentTokenCache {
	agentTokenCacheOnce.Do(func() { agentTokens = &agentTokenCache{tokens: make(map[uint]string)} })
	return agentTokens
}

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
	// Only the hash is persisted: the plaintext token is returned to the
	// admin exactly once and lives in process memory until the agent's first
	// heartbeat re-presents it. A leaked database backup must not contain
	// node control credentials.
	agent := model.Agent{
		Name:        name,
		DisplayName: displayName,
		TokenHash:   hex.EncodeToString(hash[:]),
		Status:      model.AgentStatusPending,
	}
	if err := s.db.Create(&agent).Error; err != nil {
		return nil, "", err
	}
	s.cache.set(agent.ID, token)
	return &agent, token, nil
}

// RotateToken invalidates the old token while keeping the node and its
// instance assignments. The node becomes pending until restarted with the
// returned token.
func (s *AgentService) RotateToken(id uint) (*model.Agent, string, error) {
	agent, err := s.Get(id)
	if err != nil {
		return nil, "", err
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", err
	}
	token := hex.EncodeToString(raw)
	hash := sha256.Sum256([]byte(token))
	// Rotation clears any historical plaintext and stores only the hash; the
	// new plaintext is returned once and cached in memory for this process.
	if err := s.db.Model(agent).Updates(map[string]any{
		"token_hash":   hex.EncodeToString(hash[:]),
		"status":       model.AgentStatusPending,
		"last_seen_at": nil,
	}).Error; err != nil {
		return nil, "", err
	}
	agent.TokenHash = hex.EncodeToString(hash[:])
	agent.Status = model.AgentStatusPending
	agent.LastSeenAt = nil
	s.cache.set(agent.ID, token)
	return agent, token, nil
}

func (s *AgentService) Delete(id uint) error {
	result := s.db.Delete(&model.Agent{}, id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return NotFound("被控节点不存在")
	}
	s.cache.drop(id)
	return nil
}

func (s *AgentService) Authenticate(token string) (*model.Agent, error) {
	if strings.TrimSpace(token) == "" {
		return nil, ErrUnauthorized("token 为空")
	}
	hash := sha256.Sum256([]byte(strings.TrimSpace(token)))
	var agent model.Agent
	if err := s.db.First(&agent, "token_hash = ?", hex.EncodeToString(hash[:])).Error; err != nil {
		// 常见于：节点在主控被删除、token 已轮换、或主控换了数据库重装。
		return nil, ErrUnauthorized("token 无效：请在主控「被控节点」重新生成接入指令并更新被控 --token")
	}
	// The agent authenticated itself with this token, so it is safe (and
	// required) to keep it in the process memory cache for master->agent RPC
	// instead of persisting plaintext in the database.
	s.cache.set(agent.ID, strings.TrimSpace(token))
	return &agent, nil
}

// SeedRPCToken inserts a token into the process memory cache AND syncs the
// row's token_hash to match, keeping the pair consistent exactly like the
// production create/rotate/authenticate paths do. It exists solely for
// tests that construct agents directly (bypassing the register flow);
// production code must never call it. Without the hash sync, RPCToken's
// multi-replica verification (cache vs shared row) would rightly reject
// the seeded token as a stale credential.
func (s *AgentService) SeedRPCToken(agentID uint, token string) {
	sum := sha256.Sum256([]byte(token))
	if err := s.db.Model(&model.Agent{}).Where("id = ?", agentID).
		Update("token_hash", hex.EncodeToString(sum[:])).Error; err != nil {
		return
	}
	s.cache.set(agentID, token)
}

// RPCToken returns the plaintext token for master->agent calls.
//
// Multi-replica safety (REV-AGENT-REPLICA-CACHE): the process cache is only
// an accelerator. The shared agents.token_hash row is the authority: when
// the cached token no longer hashes to the current row (a peer replica
// rotated it, or the agent was deleted), the entry is dropped and the
// caller gets the explicit recoverable error instead of the master sending
// a rotated-away credential to the node. The cache repairs itself on the
// agent's next authenticated heartbeat (any replica that serves it caches
// the new token).
//
// The verification hash is computed in-process and compared against the
// single row read, so the hot path costs one SELECT by primary key — no
// plaintext ever lives in the database.
func (s *AgentService) RPCToken(agentID uint) (string, error) {
	if tok, ok := s.cache.get(agentID); ok {
		if s.tokenMatchesRow(agentID, tok) {
			return tok, nil
		}
		// Stale entry: another replica rotated the credential or deleted
		// the agent. Never send the old token to the node.
		s.cache.drop(agentID)
	}
	return "", Conflict("被控 token 已在其它副本轮换或节点被删除：节点心跳后自动恢复（约 30 秒），或重新生成接入指令")
}

// tokenMatchesRow reports whether the cached plaintext token still matches
// the agent's current token_hash. A missing row (deleted agent) is a
// mismatch by definition.
func (s *AgentService) tokenMatchesRow(agentID uint, token string) bool {
	var agent model.Agent
	if err := s.db.Select("id", "token_hash").First(&agent, agentID).Error; err != nil {
		return false
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:]) == agent.TokenHash
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
	// The plaintext token is deliberately NOT written to the database
	// anymore (SC-03): the heartbeat call already authenticated through
	// Authenticate(), which keeps the presented token in the process memory
	// cache for master->agent RPC. Persisting it would put a node control
	// credential into every database backup.
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
