package service

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/SakuraOpenSource/virtualis/internal/model"
)

// IPPoolEntryView 是带实例名的对外条目视图。
type IPPoolEntryView struct {
	model.IPPoolEntry
	InstanceName string `json:"instance_name,omitempty"`
}

// IPPoolOverview 是单个被控节点的地址池总览。
type IPPoolOverview struct {
	AgentID   uint              `json:"agent_id"`
	AgentName string            `json:"agent_name"`
	Gateway   string            `json:"gateway"`
	Prefix    int               `json:"prefix"`
	DNS       []string          `json:"dns"`
	Interface string            `json:"interface"`
	Note      string            `json:"note"`
	Total     int               `json:"total"`
	Free      int               `json:"free"`
	Assigned  int               `json:"assigned"`
	Disabled  int               `json:"disabled"`
	Entries   []IPPoolEntryView `json:"entries"`
}

// FreeIPEntry 是「可分配地址」的展开视图：网关/前缀/DNS/接口均按
// 条目覆盖 → 池默认 → 全局默认的优先级解析完成，前端选中后可直接生成
// 独立 IP 模式的网络配置。
type FreeIPEntry struct {
	ID        uint     `json:"id"`
	IP        string   `json:"ip"`
	CIDR      string   `json:"cidr"`
	Gateway   string   `json:"gateway"`
	DNS       []string `json:"dns"`
	Interface string   `json:"interface"`
	Note      string   `json:"note"`
}

// IPPoolInput 是地址池默认参数的保存入参。
type IPPoolInput struct {
	Gateway   string   `json:"gateway"`
	Prefix    int      `json:"prefix"`
	DNS       []string `json:"dns"`
	Interface string   `json:"interface"`
	Note      string   `json:"note"`
}

// AddIPPoolEntriesInput 是批量添加地址的入参；IPs 中每一项可以是单个
// IPv4（1.2.3.4）或同一 /24 内的闭区间（1.2.3.10-1.2.3.20）。
type AddIPPoolEntriesInput struct {
	IPs     []string `json:"ips"`
	Gateway string   `json:"gateway"`
	Prefix  int      `json:"prefix"`
	Note    string   `json:"note"`
}

// AddIPPoolEntriesResult 汇总批量添加结果：成功数与逐条跳过原因。
type AddIPPoolEntriesResult struct {
	Created int                 `json:"created"`
	Skipped []IPPoolSkippedItem `json:"skipped"`
}

// IPPoolSkippedItem 是一条被跳过的地址及其原因。
type IPPoolSkippedItem struct {
	IP     string `json:"ip"`
	Reason string `json:"reason"`
}

// UpdateIPPoolEntryInput 是条目编辑入参；nil 字段保持不变。
type UpdateIPPoolEntryInput struct {
	Note    *string `json:"note"`
	Status  *string `json:"status"`
	Gateway *string `json:"gateway"`
	Prefix  *int    `json:"prefix"`
}

const (
	maxIPPoolAddPerRequest = 1024
	// maxIPPoolEntriesPerAgent 是单节点池容量上限，防御性上限。
	maxIPPoolEntriesPerAgent = 8192
)

// poolDefaults 读取被控的池默认参数；未配置时返回内置默认值。
func (s *VirtualisService) poolDefaults(agentID uint) (model.IPPool, error) {
	var pool model.IPPool
	err := s.db.Where("agent_id = ?", agentID).First(&pool).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.IPPool{AgentID: agentID, Prefix: model.DefaultIPPoolPrefix}, nil
	}
	return pool, err
}

// ListIPPools 返回全部被控节点的地址池总览（含条目与占用实例名）。
func (s *VirtualisService) ListIPPools() ([]IPPoolOverview, error) {
	var agents []model.Agent
	if err := s.db.Order("id ASC").Find(&agents).Error; err != nil {
		return nil, err
	}
	overviews := make([]IPPoolOverview, 0, len(agents))
	for _, agent := range agents {
		overview, err := s.ippoolOverview(agent)
		if err != nil {
			return nil, err
		}
		overviews = append(overviews, overview)
	}
	return overviews, nil
}

// IPPoolOverviewForAgent 返回单个被控节点的地址池总览。
func (s *VirtualisService) IPPoolOverviewForAgent(agentID uint) (*IPPoolOverview, error) {
	agent, err := NewAgentService(s.db).Get(agentID)
	if err != nil {
		return nil, err
	}
	overview, err := s.ippoolOverview(*agent)
	if err != nil {
		return nil, err
	}
	return &overview, nil
}

func (s *VirtualisService) ippoolOverview(agent model.Agent) (IPPoolOverview, error) {
	pool, err := s.poolDefaults(agent.ID)
	if err != nil {
		return IPPoolOverview{}, err
	}
	var entries []model.IPPoolEntry
	if err := s.db.Where("agent_id = ?", agent.ID).Find(&entries).Error; err != nil {
		return IPPoolOverview{}, err
	}
	names := s.instanceNames(entries)
	views := make([]IPPoolEntryView, 0, len(entries))
	overview := IPPoolOverview{
		AgentID:   agent.ID,
		AgentName: agentDisplayName(agent),
		Gateway:   pool.Gateway,
		Prefix:    normalizePoolPrefix(pool.Prefix),
		DNS:       pool.DNS,
		Interface: pool.Interface,
		Note:      pool.Note,
	}
	if overview.DNS == nil {
		overview.DNS = []string{}
	}
	for _, entry := range entries {
		overview.Total++
		switch entry.Status {
		case model.IPPoolStatusFree:
			overview.Free++
		case model.IPPoolStatusAssigned:
			overview.Assigned++
		case model.IPPoolStatusDisabled:
			overview.Disabled++
		}
		view := IPPoolEntryView{IPPoolEntry: entry}
		if entry.InstanceID != nil {
			view.InstanceName = names[*entry.InstanceID]
		}
		views = append(views, view)
	}
	sort.SliceStable(views, func(i, j int) bool { return ipSortKey(views[i].IP) < ipSortKey(views[j].IP) })
	overview.Entries = views
	return overview, nil
}

func (s *VirtualisService) instanceNames(entries []model.IPPoolEntry) map[uint]string {
	ids := make([]uint, 0, len(entries))
	for _, entry := range entries {
		if entry.InstanceID != nil {
			ids = append(ids, *entry.InstanceID)
		}
	}
	names := make(map[uint]string, len(ids))
	if len(ids) == 0 {
		return names
	}
	var instances []model.Instance
	if err := s.db.Select("id", "name", "display_name").Where("id IN ?", ids).Find(&instances).Error; err != nil {
		return names
	}
	for _, instance := range instances {
		if instance.DisplayName != "" {
			names[instance.ID] = instance.DisplayName
		} else {
			names[instance.ID] = instance.Name
		}
	}
	return names
}

func agentDisplayName(agent model.Agent) string {
	if strings.TrimSpace(agent.DisplayName) != "" {
		return agent.DisplayName
	}
	return agent.Name
}

// SaveIPPoolDefaults 保存（upsert）被控的池默认参数。
func (s *VirtualisService) SaveIPPoolDefaults(agentID uint, in IPPoolInput) (*IPPoolOverview, error) {
	if _, err := NewAgentService(s.db).Get(agentID); err != nil {
		return nil, err
	}
	gateway := strings.TrimSpace(in.Gateway)
	if gateway != "" {
		ip := net.ParseIP(gateway)
		if ip == nil || ip.To4() == nil {
			return nil, BadRequest("网关地址格式无效")
		}
		gateway = ip.String()
	}
	if in.Prefix < 0 || in.Prefix > 32 {
		return nil, BadRequest("子网前缀需在 0-32 之间")
	}
	if in.Prefix > 0 && in.Prefix < 8 {
		return nil, BadRequest("子网前缀过小（至少 /8）")
	}
	dns := make(model.StringList, 0, len(in.DNS))
	for _, item := range in.DNS {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if net.ParseIP(item) == nil {
			return nil, BadRequest("DNS 地址格式无效：%s", item)
		}
		dns = append(dns, item)
	}
	if len(dns) > 4 {
		return nil, BadRequest("DNS 最多填写 4 个地址")
	}
	iface := strings.TrimSpace(in.Interface)
	if iface != "" && !validInterfaceName(iface) {
		return nil, BadRequest("网卡名称格式无效")
	}
	note := strings.TrimSpace(in.Note)
	if len(note) > 255 {
		return nil, BadRequest("备注过长")
	}
	var pool model.IPPool
	err := s.db.Where("agent_id = ?", agentID).First(&pool).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		pool = model.IPPool{AgentID: agentID}
		err = nil
	}
	if err != nil {
		return nil, err
	}
	pool.Gateway = gateway
	pool.Prefix = in.Prefix
	pool.DNS = dns
	pool.Interface = iface
	pool.Note = note
	if pool.ID == 0 {
		if err := s.db.Create(&pool).Error; err != nil {
			return nil, err
		}
	} else if err := s.db.Save(&pool).Error; err != nil {
		return nil, err
	}
	return s.IPPoolOverviewForAgent(agentID)
}

// AddIPPoolEntries 批量添加地址；重复项按逐条原因跳过而不是整体失败。
func (s *VirtualisService) AddIPPoolEntries(agentID uint, in AddIPPoolEntriesInput) (*AddIPPoolEntriesResult, error) {
	if _, err := NewAgentService(s.db).Get(agentID); err != nil {
		return nil, err
	}
	if len(in.IPs) == 0 {
		return nil, BadRequest("请至少填写一个 IP")
	}
	if len(in.IPs) > maxIPPoolAddPerRequest {
		return nil, BadRequest("单次最多添加 %d 项", maxIPPoolAddPerRequest)
	}
	gateway := strings.TrimSpace(in.Gateway)
	if gateway != "" {
		ip := net.ParseIP(gateway)
		if ip == nil || ip.To4() == nil {
			return nil, BadRequest("网关地址格式无效")
		}
		gateway = ip.String()
	}
	if in.Prefix < 0 || in.Prefix > 32 {
		return nil, BadRequest("子网前缀需在 0-32 之间")
	}
	note := strings.TrimSpace(in.Note)
	if len(note) > 255 {
		return nil, BadRequest("备注过长")
	}

	// 展开输入并去重（保持首次出现顺序）。
	expanded := make([]string, 0, len(in.IPs))
	seen := make(map[string]bool)
	result := &AddIPPoolEntriesResult{Skipped: []IPPoolSkippedItem{}}
	for _, token := range in.IPs {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		ips, err := parseIPSpecToken(token)
		if err != nil {
			result.Skipped = append(result.Skipped, IPPoolSkippedItem{IP: token, Reason: err.Error()})
			continue
		}
		for _, ip := range ips {
			if seen[ip] {
				result.Skipped = append(result.Skipped, IPPoolSkippedItem{IP: ip, Reason: "本次提交内重复"})
				continue
			}
			seen[ip] = true
			expanded = append(expanded, ip)
		}
	}

	var existingCount int64
	if err := s.db.Model(&model.IPPoolEntry{}).Where("agent_id = ?", agentID).Count(&existingCount).Error; err != nil {
		return nil, err
	}
	var existing []model.IPPoolEntry
	if err := s.db.Where("agent_id = ?", agentID).Find(&existing).Error; err != nil {
		return nil, err
	}
	taken := make(map[string]bool, len(existing))
	for _, entry := range existing {
		taken[entry.IP] = true
	}
	for _, ip := range expanded {
		if taken[ip] {
			result.Skipped = append(result.Skipped, IPPoolSkippedItem{IP: ip, Reason: "已存在于池中"})
			continue
		}
		if existingCount+int64(result.Created) >= maxIPPoolEntriesPerAgent {
			result.Skipped = append(result.Skipped, IPPoolSkippedItem{IP: ip, Reason: "超出单节点池容量上限"})
			continue
		}
		entry := model.IPPoolEntry{
			AgentID: agentID,
			IP:      ip,
			Gateway: gateway,
			Prefix:  in.Prefix,
			Status:  model.IPPoolStatusFree,
			Note:    note,
		}
		if err := s.db.Create(&entry).Error; err != nil {
			result.Skipped = append(result.Skipped, IPPoolSkippedItem{IP: ip, Reason: "写入失败"})
			continue
		}
		taken[ip] = true
		result.Created++
	}
	return result, nil
}

// UpdateIPPoolEntry 编辑条目；已分配条目只允许「释放」（status=free）。
func (s *VirtualisService) UpdateIPPoolEntry(id uint, in UpdateIPPoolEntryInput) (*model.IPPoolEntry, error) {
	var entry model.IPPoolEntry
	if err := s.db.First(&entry, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, NotFound("地址条目不存在")
		}
		return nil, err
	}
	oldStatus := entry.Status
	oldOwner := entry.InstanceID
	if oldOwner != nil && (in.Status != nil || in.Gateway != nil || in.Prefix != nil) {
		var owners int64
		if err := s.db.Model(&model.Instance{}).Where("id = ?", *oldOwner).Count(&owners).Error; err != nil {
			return nil, err
		}
		if owners > 0 {
			return nil, Conflict("owned addresses can only be released by permanent deletion or migration")
		}
		if in.Gateway != nil || in.Prefix != nil || in.Status == nil || *in.Status != model.IPPoolStatusFree {
			return nil, Conflict("release orphan ownership before editing")
		}
	}
	if in.Note != nil {
		note := strings.TrimSpace(*in.Note)
		if len(note) > 255 {
			return nil, BadRequest("备注过长")
		}
		entry.Note = note
	}
	if in.Gateway != nil {
		gateway := strings.TrimSpace(*in.Gateway)
		if gateway != "" {
			ip := net.ParseIP(gateway)
			if ip == nil || ip.To4() == nil {
				return nil, BadRequest("网关地址格式无效")
			}
			gateway = ip.String()
		}
		entry.Gateway = gateway
	}
	if in.Prefix != nil {
		if *in.Prefix < 0 || *in.Prefix > 32 {
			return nil, BadRequest("子网前缀需在 0-32 之间")
		}
		entry.Prefix = *in.Prefix
	}
	if in.Status != nil {
		status := strings.ToLower(strings.TrimSpace(*in.Status))
		if !model.ValidIPPoolStatus(status) {
			return nil, BadRequest("状态只支持 free/assigned/disabled")
		}
		switch status {
		case model.IPPoolStatusAssigned:
			return nil, BadRequest("不能手动把地址标记为已分配")
		case model.IPPoolStatusFree:
			// 释放占用：清空实例关联。
			entry.Status = model.IPPoolStatusFree
			entry.InstanceID = nil
			entry.AssignedAt = nil
		case model.IPPoolStatusDisabled:
			if entry.Status == model.IPPoolStatusAssigned {
				return nil, Conflict("地址已分配给实例，请先释放再停用")
			}
			entry.Status = model.IPPoolStatusDisabled
		}
	}
	query := s.db.Model(&model.IPPoolEntry{}).Where("id = ? AND status = ?", id, oldStatus)
	if oldOwner == nil {
		query = query.Where("instance_id IS NULL")
	} else {
		query = query.Where("instance_id = ?", *oldOwner)
	}
	res := query.Updates(map[string]any{"status": entry.Status, "instance_id": entry.InstanceID, "assigned_at": entry.AssignedAt, "note": entry.Note, "gateway": entry.Gateway, "prefix": entry.Prefix})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected != 1 {
		return nil, Conflict("pool ownership changed; refresh before editing")
	}
	return &entry, nil
}

// DeleteIPPoolEntry 删除条目；已分配条目的必须先在实例侧释放。
func (s *VirtualisService) DeleteIPPoolEntry(id uint) error {
	var entry model.IPPoolEntry
	if err := s.db.First(&entry, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return NotFound("地址条目不存在")
		}
		return err
	}
	if entry.Status == model.IPPoolStatusAssigned {
		return Conflict("地址已分配给实例，请先释放再删除")
	}
	res := s.db.Where("id = ? AND status <> ? AND instance_id IS NULL", id, model.IPPoolStatusAssigned).Delete(&model.IPPoolEntry{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return Conflict("address ownership changed")
	}
	return nil
}

// FreeIPPoolEntries 返回被控节点当前可用的地址（展开完有效网络参数）。
func (s *VirtualisService) FreeIPPoolEntries(agentID uint) ([]FreeIPEntry, error) {
	if _, err := NewAgentService(s.db).Get(agentID); err != nil {
		return nil, err
	}
	pool, err := s.poolDefaults(agentID)
	if err != nil {
		return nil, err
	}
	var entries []model.IPPoolEntry
	if err := s.db.Where("agent_id = ? AND status = ?", agentID, model.IPPoolStatusFree).Find(&entries).Error; err != nil {
		return nil, err
	}
	out := make([]FreeIPEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, effectiveFreeEntry(entry, pool))
	}
	sort.SliceStable(out, func(i, j int) bool { return ipSortKey(out[i].IP) < ipSortKey(out[j].IP) })
	return out, nil
}

func effectiveFreeEntry(entry model.IPPoolEntry, pool model.IPPool) FreeIPEntry {
	prefix := entry.Prefix
	if prefix <= 0 {
		prefix = pool.Prefix
	}
	prefix = normalizePoolPrefix(prefix)
	gateway := entry.Gateway
	if gateway == "" {
		gateway = pool.Gateway
	}
	dns := []string(pool.DNS)
	if dns == nil {
		dns = []string{}
	}
	return FreeIPEntry{
		ID:        entry.ID,
		IP:        entry.IP,
		CIDR:      fmt.Sprintf("%s/%d", entry.IP, prefix),
		Gateway:   gateway,
		DNS:       dns,
		Interface: pool.Interface,
		Note:      entry.Note,
	}
}

// poolEntryForCreate 在创建实例时校验并返回选中条目与池默认值。
func (s *VirtualisService) poolEntryForCreate(agentID, entryID uint) (*model.IPPoolEntry, *model.IPPool, error) {
	var entry model.IPPoolEntry
	if err := s.db.First(&entry, entryID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, NotFound("IP 池地址不存在")
		}
		return nil, nil, err
	}
	if entry.AgentID != agentID {
		return nil, nil, BadRequest("所选 IP 不属于指定被控节点")
	}
	if entry.Status != model.IPPoolStatusFree {
		return nil, nil, Conflict("所选 IP 当前不可用（已分配或已停用）")
	}
	pool, err := s.poolDefaults(agentID)
	if err != nil {
		return nil, nil, err
	}
	return &entry, &pool, nil
}

// assignPoolEntry 以 CAS 方式把条目占给实例；行数为 0 说明并发抢占。
func (s *VirtualisService) assignPoolEntry(entryID, instanceID uint) error {
	now := time.Now()
	res := s.db.Model(&model.IPPoolEntry{}).
		Where("id = ? AND status = ?", entryID, model.IPPoolStatusFree).
		Updates(map[string]any{
			"status":      model.IPPoolStatusAssigned,
			"instance_id": instanceID,
			"assigned_at": now,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return Conflict("所选 IP 已被占用，请刷新后重试")
	}
	return nil
}

// ReleaseIPPoolInstance 释放实例占用的池地址（实例删除后调用）。
func (s *VirtualisService) ReleaseIPPoolInstance(instanceID uint) error {
	return s.db.Model(&model.IPPoolEntry{}).
		Where("instance_id = ?", instanceID).
		Updates(map[string]any{"status": model.IPPoolStatusFree, "instance_id": nil, "assigned_at": nil}).
		Error
}

// parseIPSpecToken 解析单个 IPv4 或同一 /24 内的闭区间（A-B）。
func parseIPSpecToken(token string) ([]string, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, nil
	}
	if !strings.Contains(token, "-") {
		ip := net.ParseIP(token)
		if ip == nil || ip.To4() == nil {
			return nil, fmt.Errorf("无效的 IPv4 地址：%s", token)
		}
		return []string{ip.String()}, nil
	}
	parts := strings.SplitN(token, "-", 2)
	start := net.ParseIP(strings.TrimSpace(parts[0]))
	end := net.ParseIP(strings.TrimSpace(parts[1]))
	if start == nil || start.To4() == nil || end == nil || end.To4() == nil {
		return nil, fmt.Errorf("无效的 IP 区间：%s", token)
	}
	s4, e4 := start.To4(), end.To4()
	if s4[0] != e4[0] || s4[1] != e4[1] || s4[2] != e4[2] {
		return nil, fmt.Errorf("IP 区间需在同一 /24 网段内：%s", token)
	}
	from, to := int(s4[3]), int(e4[3])
	if from > to {
		return nil, fmt.Errorf("IP 区间起点大于终点：%s", token)
	}
	out := make([]string, 0, to-from+1)
	for i := from; i <= to; i++ {
		out = append(out, fmt.Sprintf("%d.%d.%d.%d", s4[0], s4[1], s4[2], i))
	}
	return out, nil
}

func normalizePoolPrefix(prefix int) int {
	if prefix <= 0 {
		return model.DefaultIPPoolPrefix
	}
	return prefix
}

// sameIPv4 比较两个 IPv4 是否一致（左侧允许带 /前缀）。
func sameIPv4(a, b string) bool {
	pa := net.ParseIP(strings.Split(strings.TrimSpace(a), "/")[0])
	pb := net.ParseIP(strings.TrimSpace(b))
	return pa != nil && pb != nil && pa.Equal(pb)
}

func validInterfaceName(name string) bool {
	if len(name) > 64 {
		return false
	}
	for _, r := range name {
		if r == '-' || r == '_' || r == '.' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			continue
		}
		return false
	}
	return true
}

// ipSortKey 把 IPv4 转成可比较的整数键；不可解析的排到最后。
func ipSortKey(ip string) uint64 {
	parsed := net.ParseIP(strings.Split(strings.TrimSpace(ip), "/")[0])
	if parsed == nil || parsed.To4() == nil {
		return 1 << 62
	}
	v4 := parsed.To4()
	return uint64(v4[0])<<24 | uint64(v4[1])<<16 | uint64(v4[2])<<8 | uint64(v4[3])
}
