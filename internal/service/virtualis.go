package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"

	"github.com/SakuraOpenSource/virtualis/internal/agentclient"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"github.com/SakuraOpenSource/virtualis/internal/storage"
)

const maxImageUploadSize = int64(64 << 30)

// VirtualisService is the master-side orchestrator. It owns the database, but
// all instance lifecycle calls are sent to the selected agent.
type VirtualisService struct {
	db       *gorm.DB
	settings *SettingService
	storage  *storage.Store

	// vncTickets 是一次性 VNC 短票：key 为随机凭证，value 绑定实例与过期时间。
	// 只活在内存里，主控重启即失效，调用方（Levis 插件）须重新领取。
	opMu             sync.Mutex
	activeOperations map[uint]bool
	vncMu            sync.Mutex
	vncTickets       map[string]vncTicket
}

type vncTicket struct {
	instanceID uint
	expires    time.Time
}

// vncTicketTTL 是短票有效期：覆盖用户从点击到 noVNC 建连的耗时即可，越短越安全。
const vncTicketTTL = 120 * time.Second

// NewVirtualisService constructs the orchestration service. All driver
// operations run on agents; the master only coordinates and persists.
func NewVirtualisService(db *gorm.DB, stores ...*storage.Store) *VirtualisService {
	var store *storage.Store
	if len(stores) > 0 {
		store = stores[0]
	}
	return &VirtualisService{db: db, settings: NewSettingService(db), storage: store, vncTickets: make(map[string]vncTicket)}
}

// DriverStatus describes drivers installed on at least one connected agent.
type DriverStatus struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Error     string `json:"error,omitempty"`
}

// ListDrivers reports agent capabilities, not software installed on the master.
func (s *VirtualisService) ListDrivers(ctx context.Context) []DriverStatus {
	var agents []model.Agent
	if err := s.db.Find(&agents).Error; err != nil {
		return unavailableDrivers("读取被控节点失败")
	}
	summary := make(map[string]bool)
	for _, name := range model.AllDrivers() {
		if name != model.DriverAuto {
			summary[name] = false
		}
	}
	for _, agent := range agents {
		if !agent.IsOnline() || strings.TrimSpace(agent.Endpoint) == "" {
			continue
		}
		for _, name := range agent.Drivers {
			summary[name] = true
		}
	}
	items := make([]DriverStatus, 0, len(model.AllDrivers()))
	for _, name := range model.AllDrivers() {
		if name == model.DriverAuto {
			continue
		}
		items = append(items, DriverStatus{Name: name, Available: summary[name]})
	}
	return items
}

func unavailableDrivers(reason string) []DriverStatus {
	items := make([]DriverStatus, 0, len(model.AllDrivers()))
	for _, name := range model.AllDrivers() {
		items = append(items, DriverStatus{Name: name, Error: reason})
	}
	return items
}

// ListInstances returns paginated instances.
func (s *VirtualisService) ListInstances(page, pageSize int) ([]model.Instance, int64, error) {
	return s.ListInstancesForOwner(page, pageSize, 0)
}
func (s *VirtualisService) ListInstancesForOwner(page, pageSize int, ownerID uint) ([]model.Instance, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	query := s.db.Model(&model.Instance{}).Where("trashed_at IS NULL")
	if ownerID != 0 {
		query = query.Where("owner_id = ?", ownerID)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []model.Instance
	offset := (page - 1) * pageSize
	if err := query.Preload("Image").Preload("Agent").Order("id DESC").Offset(offset).Limit(pageSize).Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// GetInstance returns instance by id.
func (s *VirtualisService) GetInstance(id uint) (*model.Instance, error) {
	inst, err := s.GetAnyInstance(id)
	if err == nil && inst.TrashedAt != nil {
		return nil, NotFound("instance is in recycle bin")
	}
	return inst, err
}

func (s *VirtualisService) GetAnyInstance(id uint) (*model.Instance, error) {
	var inst model.Instance
	if err := s.db.Preload("Image").Preload("Agent").Preload("NATMappings", "reservation_operation = ''").Preload("VPC").Preload("SecurityGroups", func(db *gorm.DB) *gorm.DB { return db.Order("id ASC") }).Preload("SecurityGroups.Rules", func(db *gorm.DB) *gorm.DB { return db.Order("priority ASC, id ASC") }).Preload("FirewallRules", func(db *gorm.DB) *gorm.DB { return db.Order("priority ASC, id ASC") }).First(&inst, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, NotFound("instance not found")
		}
		return nil, err
	}
	_, inst.FirewallPolicy = effectiveFirewall(&inst)
	inst.SSHPassword = inst.LoadSSHPassword()
	return &inst, nil
}

type CreateInstanceRequest struct {
	SecurityGroupIDs []uint              `json:"security_group_ids"`
	Name             string              `json:"name"`
	Driver           string              `json:"driver"`
	Type             string              `json:"type"`
	Spec             model.InstanceSpec  `json:"spec"`
	Network          model.NetworkConfig `json:"network"`
	ImageID          *uint               `json:"image_id"`
	AgentID          *uint               `json:"agent_id"`
	// MaxNATMappings 是允许创建的 NAT 映射上限，0 表示不限。
	MaxNATMappings int `json:"max_nat_mappings"`
	// IPPoolEntryID 指定池地址；省略且 IPv4 为空时自动分配所选节点地址。
	IPPoolEntryID *uint `json:"ip_pool_entry_id"`
	VPCID         *uint `json:"vpc_id"`
	// AutoPassword 为 true（默认）时生成随机 root 密码并存库供管理页查看。
	AutoPassword *bool `json:"auto_password"`
}

// CreateInstance records an instance and provisions it on the selected agent.
func (s *VirtualisService) CreateInstance(ctx context.Context, req CreateInstanceRequest) (result *model.Instance, err error) {
	if err := ValidateInstanceName(req.Name); err != nil {
		return nil, err
	}
	if err := validateSecurityGroupIDs(req.SecurityGroupIDs); err != nil {
		return nil, err
	}
	if req.AgentID == nil || *req.AgentID == 0 {
		return nil, BadRequest("必须选择被控节点，主控不负责创建实例")
	}
	agentSvc := NewAgentService(s.db)
	agent, err := agentSvc.Get(*req.AgentID)
	if err != nil {
		return nil, err
	}
	if !agent.IsOnline() {
		return nil, Conflict("被控节点当前不在线")
	}
	client, err := s.agentClient(agent)
	if err != nil {
		return nil, err
	}
	capabilities, err := client.Drivers(ctx)
	if err != nil {
		return nil, agentFailure(err)
	}

	driverName := strings.ToLower(strings.TrimSpace(req.Driver))
	if driverName == "" {
		driverName = strings.ToLower(strings.TrimSpace(s.settings.Virtualis().DefaultDriver))
	}
	if driverName == "" {
		driverName = model.DriverAuto
	}
	if !model.ValidDriver(driverName) {
		return nil, BadRequest("invalid driver %q", driverName)
	}
	if driverName != model.DriverAuto && !capabilityAvailable(capabilities, driverName) {
		return nil, BadRequest("被控节点未安装驱动 %q", driverName)
	}
	if len(req.SecurityGroupIDs) > 0 {
		if err := client.RequireFirewallPolicy(ctx, agentclient.Instance{Driver: driverName, FirewallPolicy: &model.FirewallPolicy{Ingress: "drop", Egress: "accept"}}); err != nil {
			return nil, Conflict("无法创建安全组实例：%s", err)
		}
	}

	def := s.settings.Virtualis()
	spec := req.Spec
	// 毫核模式下 CPU 由 NormalizeInstanceSpec 按毫核向上取整得出，这里
	// 不再套用整核默认值，避免覆盖毫核换算结果。
	if spec.CPU == 0 && spec.CPUMilli <= 0 {
		spec.CPU = def.DefaultCPU
	}
	if spec.MemoryMB == 0 {
		spec.MemoryMB = def.DefaultMemory
	}
	if spec.DiskGB == 0 {
		spec.DiskGB = def.DefaultDisk
	}
	if spec.Arch == "" {
		spec.Arch = def.DefaultArch
	}
	spec, err = model.NormalizeInstanceSpec(spec)
	if err != nil {
		return nil, BadRequest("%s", err.Error())
	}
	// 独立 IP 池：从池内选择地址时自动生成网络配置（CIDR/网关/DNS/
	// 挂载接口）。池默认参数先于站点默认网卡生效，显式填写始终优先。
	var poolEntry *model.IPPoolEntry
	if req.IPPoolEntryID != nil && *req.IPPoolEntryID > 0 {
		if !strings.EqualFold(strings.TrimSpace(req.Network.Mode), model.NetworkModeDedicated) {
			return nil, BadRequest("IP 池选择仅适用于独立 IP 模式")
		}
		entry, pool, poolErr := s.poolEntryForCreate(agent.ID, *req.IPPoolEntryID)
		if poolErr != nil {
			return nil, poolErr
		}
		poolEntry = entry
		prefix := entry.Prefix
		if prefix <= 0 {
			prefix = pool.Prefix
		}
		prefix = normalizePoolPrefix(prefix)
		if strings.TrimSpace(req.Network.IPv4) == "" {
			req.Network.IPv4 = fmt.Sprintf("%s/%d", entry.IP, prefix)
		} else if !sameIPv4(req.Network.IPv4, entry.IP) {
			return nil, BadRequest("网络配置中的 IPv4 与所选池内地址不一致")
		}
		if strings.TrimSpace(req.Network.Gateway) == "" {
			if entry.Gateway != "" {
				req.Network.Gateway = entry.Gateway
			} else {
				req.Network.Gateway = pool.Gateway
			}
		}
		if len(req.Network.DNS) == 0 && len(pool.DNS) > 0 {
			req.Network.DNS = append([]string(nil), pool.DNS...)
		}
		if strings.TrimSpace(req.Network.Bridge) == "" && pool.Interface != "" {
			req.Network.Bridge = pool.Interface
		}
	}
	// 默认网卡只作用于 dedicated 模式；NAT 必须继续使用 incusbr0/
	if strings.EqualFold(strings.TrimSpace(req.Network.Mode), model.NetworkModeVPC) {
		if req.VPCID == nil || *req.VPCID == 0 {
			return nil, BadRequest("VPC 模式必须选择网络")
		}
		vpc, vpcErr := s.GetVPC(*req.VPCID)
		if vpcErr != nil {
			return nil, vpcErr
		}
		if vpc.AgentID != agent.ID {
			return nil, BadRequest("VPC 不属于所选节点")
		}
		if driverName == model.DriverAuto {
			driverName = vpc.Driver
		}
		if driverName != vpc.Driver {
			return nil, BadRequest("VPC 与实例驱动必须一致")
		}
		req.Network.Bridge = vpc.Name
		if req.Network.Gateway == "" {
			req.Network.Gateway = vpc.Gateway
		}
		if len(req.Network.DNS) == 0 {
			req.Network.DNS = append([]string(nil), vpc.DNS...)
		}
	} else if req.VPCID != nil {
		return nil, BadRequest("只有 VPC 模式可以选择 VPC")
	}
	// virbr0，none 模式不应注入任何挂载目标。显式 bridge 始终优先。
	if strings.EqualFold(strings.TrimSpace(req.Network.Mode), model.NetworkModeDedicated) && strings.TrimSpace(req.Network.Bridge) == "" {
		req.Network.Bridge = def.DefaultNIC
	}
	network, err := model.NormalizeNetworkConfig(req.Network)
	if err != nil {
		return nil, BadRequest("%s", err.Error())
	}
	// 独立 IP 校验实际上联与主机地址冲突；池容量由事务内选取确认。
	var hostNetwork *agentclient.HostNetworkSummary
	autoPool := network.Mode == model.NetworkModeDedicated && (req.IPPoolEntryID == nil || *req.IPPoolEntryID == 0) && strings.TrimSpace(network.IPv4) == ""
	if network.Mode == model.NetworkModeDedicated {
		summary, hnErr := client.HostNetwork(ctx)
		if hnErr != nil {
			return nil, agentFailure(hnErr)
		}
		hostNetwork = summary
		if !autoPool {
			if err := validateDedicatedHost(network, summary); err != nil {
				return nil, err
			}
		}
	}
	if network.IPv4 != "" {
		taken, dupErr := s.dedicatedIPTaken(agent.ID, network.IPv4, 0)
		if dupErr != nil {
			return nil, dupErr
		}
		if taken {
			return nil, Conflict("独立 IP %s 已被其它实例占用", network.IPv4)
		}
	}

	var image *model.Image
	if req.ImageID != nil {
		image, err = s.GetImage(*req.ImageID)
		if err != nil {
			return nil, err
		}
		if image.Status != "" && image.Status != model.ImageStatusAvailable {
			return nil, Conflict("镜像当前不可用")
		}
		if driverName != model.DriverAuto && image.Driver != "" && image.Driver != model.DriverAuto && image.Driver != driverName {
			return nil, BadRequest("镜像驱动 %q 与实例驱动 %q 不匹配", image.Driver, driverName)
		}
	}

	instance := &model.Instance{
		Name:           strings.TrimSpace(req.Name),
		Driver:         driverName,
		Type:           normalizeInstanceType(req.Type),
		Spec:           spec,
		Network:        network,
		Status:         model.InstanceStatusCreating,
		ImageID:        req.ImageID,
		AgentID:        req.AgentID,
		MaxNATMappings: req.MaxNATMappings,
		VPCID:          req.VPCID,
	}
	if instance.MaxNATMappings < 0 {
		instance.MaxNATMappings = 0
	}
	// 默认生成随机 root 密码：管理页可查看并连接，NAT 模式再自动配一条
	// 22 端口映射。AutoPassword 缺省视为 true。
	autoPassword := req.AutoPassword == nil || *req.AutoPassword
	if autoPassword {
		instance.StoreSSHPassword(GeneratePassword(16))
	}
	operationID := newOperationID()
	now := time.Now().UTC()
	instance.BusyOperation = operationID
	instance.BusyAction = "create"
	instance.BusySince = &now
	baseNetwork := network
	if err = s.createReservationTransaction(ctx, func(tx *gorm.DB) error {
		instance.Base = model.Base{}
		network = baseNetwork
		// 先取节点行写锁，避免 SQLite 读事务升级死锁；同节点池选取、
		// 地址 CAS 和实例创建在同一事务内。其它数据库也串行化同节点分配。
		if e := tx.Model(&model.Agent{}).Where("id = ?", agent.ID).UpdateColumn("id", gorm.Expr("id")).Error; e != nil {
			return e
		}
		if autoPool {
			var entry model.IPPoolEntry
			if e := tx.Where("agent_id = ? AND status = ? AND instance_id IS NULL", agent.ID, model.IPPoolStatusFree).Order("id ASC").First(&entry).Error; e != nil {
				if errors.Is(e, gorm.ErrRecordNotFound) {
					return Conflict("所选节点没有可用的独立 IP 池地址")
				}
				return e
			}
			pool, e := NewVirtualisService(tx).poolDefaults(agent.ID)
			if e != nil {
				return e
			}
			effective := effectiveFreeEntry(entry, pool)
			network.IPv4 = effective.CIDR
			if network.Gateway == "" {
				network.Gateway = effective.Gateway
			}
			if len(network.DNS) == 0 {
				network.DNS = effective.DNS
			}
			if strings.TrimSpace(req.Network.Bridge) == "" && effective.Interface != "" {
				network.Bridge = effective.Interface
			}
			network, e = model.NormalizeNetworkConfig(network)
			if e != nil {
				return BadRequest("%s", e)
			}
			if e = validateDedicatedHost(network, hostNetwork); e != nil {
				return e
			}
			taken, e := NewVirtualisService(tx).dedicatedIPTaken(agent.ID, network.IPv4, 0)
			if e != nil {
				return e
			}
			if taken {
				return Conflict("池地址已被其它实例占用")
			}
			instance.Network = network
			poolEntry = &entry
		}
		if instance.VPCID != nil {
			if e := reserveVPCReference(tx, *instance.VPCID); e != nil {
				return e
			}
		}
		if e := tx.Create(instance).Error; e != nil {
			return e
		}
		if len(req.SecurityGroupIDs) > 0 {
			if e := bindSecurityGroups(tx, instance.ID, req.SecurityGroupIDs); e != nil {
				return e
			}
			loaded, e := NewVirtualisService(tx).GetAnyInstance(instance.ID)
			if e != nil {
				return e
			}
			instance.SecurityGroups = loaded.SecurityGroups
			instance.FirewallRevision = loaded.FirewallRevision
			instance.FirewallPending = loaded.FirewallPending
		}
		if poolEntry != nil {
			return NewVirtualisService(tx).assignPoolEntry(poolEntry.ID, instance.ID)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	guard := &operationGuard{s: s, id: instance.ID, token: operationID, action: "create"}
	defer guard.finishInstance(&err, &result)
	appendOperationLog(s.db, instance.ID, operationID, model.OperationCreate, "database", model.OperationSuccess, "实例记录已创建", nil)
	// 池地址占位：CAS 失败说明并发抢占，回滚刚创建的实例记录。

	// 自动 SSH 映射：NAT 模式且有密码时建一条 TCP 22 转发，不计入上限。
	if autoPassword && network.Mode == model.NetworkModeNAT {
		hostPort, portErr := s.allocateNATPort(*req.AgentID)
		if portErr == nil {
			mapping := model.NATMapping{
				InstanceID: instance.ID,
				AgentID:    *req.AgentID,
				Protocol:   "tcp",
				HostPort:   hostPort,
				GuestPort:  22,
				Remark:     "SSH（自动）",
				Auto:       true,
			}
			if err := s.db.Create(&mapping).Error; err == nil {
				instance.NATMappings = append(instance.NATMappings, mapping)
			}
		}
	}

	reader, filename, openErr := s.openImage(image)
	if openErr != nil {
		_ = s.db.Model(instance).Update("status", model.InstanceStatusError).Error
		instance.Status = model.InstanceStatusError
		return instance, openErr
	}
	if reader != nil {
		defer reader.Close()
	}
	wireInstance := toWireInstance(instance, image)
	// 面板生成的初始 root 密码只随创建请求下发一次。
	wireInstance.RootPassword = instance.LoadSSHPassword()
	var extraReader io.ReadCloser
	var extraName string
	if extraReader, extraName, err = s.openExtraImage(image); err != nil {
		_ = s.db.Model(instance).Update("status", model.InstanceStatusError).Error
		instance.Status = model.InstanceStatusError
		return instance, err
	}
	if extraReader != nil {
		defer extraReader.Close()
	}
	remote, remoteErr := client.CreateInstance(ctx, wireInstance, toWireImage(image), reader, filename, extraReader, extraName)
	if remoteErr != nil {
		_ = s.db.Model(instance).Update("status", model.InstanceStatusError).Error
		instance.Status = model.InstanceStatusError
		return instance, agentFailure(remoteErr)
	}
	applyWireInstance(instance, remote)
	mergeRemoteNetwork(instance, remote)
	if !model.ValidInstanceStatus(instance.Status) || instance.Status == model.InstanceStatusCreating {
		instance.Status = model.InstanceStatusStopped
	}
	// map 形式的 Updates 不会触发字段上的 JSON 序列化器，NetworkConfig
	// 结构体必须先自己序列化成字符串才能写进 TEXT 列。
	networkJSON, mErr := json.Marshal(instance.Network)
	if mErr != nil {
		return nil, mErr
	}
	if err := s.db.Model(instance).Updates(map[string]any{
		"status": instance.Status, "driver": instance.Driver, "network": string(networkJSON), "ip": instanceDisplayIP(instance), "observed_ip": instance.ObservedIP,
	}).Error; err != nil {
		return nil, err
	}
	appendOperationLog(s.db, instance.ID, operationID, model.OperationCreate, "agent", model.OperationSuccess,
		fmt.Sprintf("实例创建完成，状态 %s，配置 IPv4 %s", instance.Status, primaryConfiguredIP(instance.Network)), nil)
	// 首次密码注入在被控后台执行：挂一个短轮询，注入完成后把 ssh_ready
	// 回写为 true，让面板与下游（Levis 等）不必依赖手动“配置网络”。
	if wireInstance.RootPassword != "" {
		go s.watchSSHReady(instance.ID, instance.Agent)
	}
	return s.GetInstance(instance.ID)
}

// watchSSHReady observes readiness only, and only while the instance is
// unfenced. It deliberately does NOT take its own beginOperation fence: this
// is a best-effort background observation of first-boot password injection,
// and a watcher silently holding the lifecycle fence would block exactly the
// power/restore operations that are supposed to outrank it. The trade-off is
// documented: while another operation holds the fence the watcher stops
// polling entirely (the fenced operation's own status reconciliation will
// converge ssh_ready), and the watcher re-reads the instance every iteration
// so a changed AgentID/ownership is never sent to a stale client.
func (s *VirtualisService) watchSSHReady(instanceID uint, agent *model.Agent) {
	if agent == nil {
		return
	}
	// 6 minutes of patience: minimal images install openssh-server on first
	// boot (apt update + install), injection usually takes 2-4 minutes.
	for attempt := 0; attempt < 60; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(6 * time.Second):
			}
		}
		instance, err := s.GetInstance(instanceID)
		if err != nil {
			return // instance deleted
		}
		if instance.SSHReady {
			return
		}
		// A durable fence owned by another operation means that operation is
		// reconciling the instance right now; a background watcher must not
		// race it with parallel agent status calls or readiness writes.
		if instance.BusyOperation != "" {
			return
		}
		// Resolve the client from the CURRENT row, not the captured agent:
		// during the watcher's lifetime the instance may migrate or be
		// restored to a different node.
		if instance.Agent == nil {
			return
		}
		client, err := s.agentClient(instance.Agent)
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		remote, err := client.Status(ctx, toWireInstance(instance, instance.Image))
		cancel()
		if err != nil {
			continue
		}
		if remote.SSHReady {
			// The write is conditioned on the instance still being unfenced
			// and still owned by this agent, so a migration/restore that
			// started mid-call cannot have its result clobbered.
			agentID := uint(0)
			if instance.AgentID != nil {
				agentID = *instance.AgentID
			}
			// The write is conditioned on the instance still being unfenced,
			// still owned by this agent, AND still the same guest generation
			// the status reply was captured for. busy='' + same agent alone is
			// an ABA across a same-node reinstall (fence comes back empty,
			// agent unchanged); the generation clause makes the stale reply a
			// no-op instead of overwriting the new guest's readiness.
			res := s.db.Model(&model.Instance{}).
				Where("id = ? AND busy_operation = '' AND agent_id = ? AND lifecycle_generation = ?", instanceID, agentID, instance.LifecycleGeneration).
				Update("ssh_ready", true)
			if res.Error != nil || res.RowsAffected != 1 {
				return
			}
			return
		}
	}
}

// dedicatedIPTaken 报告同一被控上是否已有实例占用该独立 IP。
// excludeID 用于更新场景预留；当前创建流程传 0。
func (s *VirtualisService) dedicatedIPTaken(agentID uint, ip string, excludeID uint) (bool, error) {
	ipAddr := net.ParseIP(strings.Split(ip, "/")[0])
	if ipAddr == nil {
		return false, BadRequest("IPv4 地址格式无效")
	}
	var items []model.Instance
	if err := s.db.Where("agent_id = ? AND id <> ?", agentID, excludeID).Find(&items).Error; err != nil {
		return false, err
	}
	for _, item := range items {
		if item.Network.IPv4 == "" {
			continue
		}
		other := net.ParseIP(strings.Split(item.Network.IPv4, "/")[0])
		if other != nil && other.Equal(ipAddr) {
			return true, nil
		}
	}
	// 池内已分配条目代表未证实的远程占用（失败保留/迁移中），同样视为冲突。
	var entries []model.IPPoolEntry
	if err := s.db.Where("agent_id = ? AND status = ? AND instance_id IS NOT NULL", agentID, model.IPPoolStatusAssigned).Find(&entries).Error; err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.InstanceID != nil && *entry.InstanceID == excludeID {
			continue
		}
		if other := net.ParseIP(entry.IP); other != nil && other.Equal(ipAddr) {
			return true, nil
		}
	}
	return false, nil
}

// AgentHostNetwork 拉取被控主机的网卡清单，供创建实例时选择独立 IP
// 的挂载接口，并判断该节点是否满足独立 IP 模式条件。
func (s *VirtualisService) AgentHostNetwork(ctx context.Context, agentID uint) (*agentclient.HostNetworkSummary, error) {
	agentSvc := NewAgentService(s.db)
	agent, err := agentSvc.Get(agentID)
	if err != nil {
		return nil, err
	}
	client, err := s.agentClient(agent)
	if err != nil {
		return nil, err
	}
	summary, err := client.HostNetwork(ctx)
	if err != nil {
		return nil, agentFailure(err)
	}
	return summary, nil
}

func normalizeInstanceType(kind string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == model.InstanceTypeVM {
		return model.InstanceTypeVM
	}
	return model.InstanceTypeContainer
}

func capabilityAvailable(items []agentclient.Driver, name string) bool {
	for _, item := range items {
		if item.Name == name && item.Available {
			return true
		}
	}
	return false
}

// PowerInstance executes a power action. For reinstall the optional imageID
// selects a different base image; it is applied only after the fence is held.
// callerRef (the plugin's X-Levis-Operation-ID) is persisted with the
// operation logs for upstream correlation.
func (s *VirtualisService) PowerInstance(ctx context.Context, id uint, action string, imageID *uint, callerRef ...string) (result *model.Instance, err error) {
	// The caller id must be part of the fence claim itself: claiming first
	// and recording the ref afterwards left a window where a same-id retry
	// could not be matched and re-ran the destructive RPC.
	ref := ""
	for _, r := range callerRef {
		ref = r
	}
	// The idempotency record keys on the CONCRETE action ("stop"/"start"/
	// "reinstall"), not the route-level "power": one upstream id reused for
	// start after stop would otherwise be treated as a legal replay of a
	// different destructive operation.
	recordAction := model.OperationPower + ":" + action
	guard, err := s.beginOperationWithRef(ctx, id, recordAction, ref)
	if err != nil {
		return nil, err
	}
	if guard.replay {
		// Idempotent retry of an already-succeeded operation: return the
		// current instance state; never re-send the RPC.
		return s.GetInstance(id)
	}
	// finishInstance also strips the released busy token from the returned
	// instance; returning the token made clients keep treating a completed
	// operation as permanently busy.
	defer guard.finishInstance(&err, &result)
	operationID := guard.token
	appendOperationLog(s.db, id, operationID, model.OperationPower, "start", model.OperationRunning, "开始执行 "+action, nil)
	if !model.ValidAction(action) {
		return nil, BadRequest("invalid action %q", action)
	}
	instance, err := s.GetInstance(id)
	if err != nil {
		return nil, err
	}
	if instance.Agent == nil {
		return nil, Conflict("实例没有关联被控节点")
	}
	if action == model.ActionReinstall && !s.settings.Virtualis().AllowReinstall {
		return nil, Forbidden("reinstall disabled")
	}
	// Reinstall image selection happens inside the lifecycle fence so a
	// rejected request (disabled reinstall, busy instance, invalid image)
	// can never mutate the instance's image association. When a different
	// image is selected, the instance row AND the in-memory association are
	// reloaded from the database: the preloaded Image association still
	// points at the OLD image, and using it would send the old image's
	// metadata/file to the agent while persisting the new image_id — the
	// guest gets reinstalled with the wrong OS and the subsequent GORM
	// Updates writes the stale association back over the new one.
	if action == model.ActionReinstall && imageID != nil {
		selected, err := s.GetImage(*imageID)
		if err != nil {
			return nil, err
		}
		if instance.ImageID == nil || *instance.ImageID != *imageID {
			if err := s.db.Model(&model.Instance{}).Where("id = ? AND busy_operation = ?", id, guard.token).Update("image_id", *imageID).Error; err != nil {
				return nil, err
			}
		}
		// Bind the freshly selected image regardless: the preloaded
		// association may be stale even when the id matches (image row
		// re-uploaded meanwhile). instance.Image is what openImage and
		// toWireImage consume.
		instance.Image = selected
		instance.ImageID = &selected.ID
	}
	client, err := s.agentClient(instance.Agent)
	if err != nil {
		return nil, err
	}
	var reader io.ReadCloser
	var filename string
	var extraReader io.ReadCloser
	var extraName string
	if action == model.ActionReinstall {
		reader, filename, err = s.openImage(instance.Image)
		if err != nil {
			return nil, err
		}
		if reader != nil {
			defer reader.Close()
		}
		extraReader, extraName, err = s.openExtraImage(instance.Image)
		if err != nil {
			return nil, err
		}
		if extraReader != nil {
			defer extraReader.Close()
		}
	}
	wireInstance := toWireInstance(instance, instance.Image)
	if action == model.ActionReinstall {
		// 重装会得到一个全新 guest，与创建路径一致地携带初始 root 密码，
		// 被控在重装完成后据此重做注入，保证重装出来的系统开箱即用。
		wireInstance.RootPassword = instance.LoadSSHPassword()
	}
	remote, err := client.PowerInstance(ctx, wireInstance, action, toWireImage(instance.Image), reader, filename, extraReader, extraName)
	if err != nil {
		_ = s.db.Model(instance).Update("status", model.InstanceStatusError).Error
		if action == model.ActionReinstall {
			// A failed or lost reinstall response is not proof the disk was
			// left untouched: the agent replaces the disk before replying,
			// so the guest may already be running the new image (or be half
			// written). Retain the durable fence until the intended image
			// and disk state are verified, mirroring the snapshot_restore
			// semantics for equally destructive operations. A plain
			// status=error write is an observation, not a fence.
			guard.retain = true
		}
		return nil, agentFailure(err)
	}
	applyWireInstance(instance, remote)
	mergeRemoteNetwork(instance, remote)
	// map 形式的 Updates 不触发字段上的 JSON 序列化器，网络配置先自行序列化。
	networkJSON, mErr := json.Marshal(instance.Network)
	if mErr != nil {
		return nil, mErr
	}
	updates := map[string]any{"status": instance.Status, "driver": instance.Driver, "network": string(networkJSON), "ip": instanceDisplayIP(instance), "observed_ip": instance.ObservedIP}
	// 重装等于换了个全新 guest：旧的 SSH 就绪状态作废，等后台注入完成后
	// 由 watchSSHReady 重新置 true。generation 递增让重装前起飞的旧
	// watcher 回包无法把新 guest 标成就绪（ABA 防护）。
	if action == model.ActionReinstall {
		updates["ssh_ready"] = false
		updates["lifecycle_generation"] = gorm.Expr("lifecycle_generation + 1")
	}
	if err := s.db.Model(instance).Updates(updates).Error; err != nil {
		return nil, err
	}
	if action == model.ActionReinstall && instance.LoadSSHPassword() != "" {
		go s.watchSSHReady(instance.ID, instance.Agent)
	}
	appendOperationLog(s.db, id, operationID, model.OperationPower, "complete", model.OperationSuccess, action+" 执行完成", nil)
	return s.GetInstance(instance.ID)
}

func (s *VirtualisService) RefreshStatus(ctx context.Context, id uint) (result *model.Instance, err error) {
	guard, err := s.beginOperation(ctx, id, "status")
	if err != nil {
		return nil, err
	}
	defer guard.finishInstance(&err, &result)
	instance, err := s.GetInstance(id)
	if err != nil {
		return nil, err
	}
	if instance.Agent == nil {
		return nil, Conflict("实例没有关联被控节点")
	}
	client, err := s.agentClient(instance.Agent)
	if err != nil {
		return nil, err
	}
	remote, err := client.Status(ctx, toWireInstance(instance, instance.Image))
	if err != nil {
		return nil, agentFailure(err)
	}
	applyWireInstance(instance, remote)
	mergeRemoteNetwork(instance, remote)
	if !model.ValidInstanceStatus(instance.Status) {
		return nil, BadRequest("被控返回了无效实例状态")
	}
	networkJSON, mErr := json.Marshal(instance.Network)
	if mErr != nil {
		return nil, mErr
	}
	updates := map[string]any{"status": instance.Status, "driver": instance.Driver, "network": string(networkJSON), "ip": instanceDisplayIP(instance), "observed_ip": instance.ObservedIP}
	// 被控的首次密码注入是异步的：状态轮询顺带把 ssh_ready 回写为 true，
	// 只允许 false→true，配置网络失败路径负责置回 false。
	if remote.SSHReady && !instance.SSHReady {
		instance.SSHReady = true
		updates["ssh_ready"] = true
	}
	if err := s.db.Model(instance).Updates(updates).Error; err != nil {
		return nil, err
	}
	return s.GetInstance(id)
}

func (s *VirtualisService) InstanceMetrics(ctx context.Context, id uint) (agentclient.Metrics, error) {
	instance, err := s.GetInstance(id)
	if err != nil {
		return agentclient.Metrics{}, err
	}
	if instance.Agent == nil {
		return agentclient.Metrics{}, Conflict("实例没有关联被控节点")
	}
	client, err := s.agentClient(instance.Agent)
	if err != nil {
		return agentclient.Metrics{}, err
	}
	metrics, err := client.Metrics(ctx, toWireInstance(instance, instance.Image))
	if err != nil {
		return agentclient.Metrics{}, agentFailure(err)
	}
	return metrics, nil
}

func (s *VirtualisService) InstanceNetwork(ctx context.Context, id uint) (result agentclient.NetworkStatus, err error) {
	guard, err := s.beginOperation(ctx, id, "network_status")
	if err != nil {
		return result, err
	}
	defer guard.finish(&err)
	instance, err := s.GetInstance(id)
	if err != nil {
		return agentclient.NetworkStatus{}, err
	}
	if instance.Agent == nil {
		return agentclient.NetworkStatus{}, Conflict("实例没有关联被控节点")
	}
	client, err := s.agentClient(instance.Agent)
	if err != nil {
		return agentclient.NetworkStatus{}, err
	}
	if network, err := client.Network(ctx, toWireInstance(instance, instance.Image)); err == nil {
		if ip := firstAgentNetworkIPv4(network); ip != "" {
			instance.ObservedIP = ip
			instance.IP = ip
			_ = s.db.Model(instance).Updates(map[string]any{"ip": ip, "observed_ip": ip, "network_error": ""}).Error
		}
		return network, nil
	} else {
		return agentclient.NetworkStatus{}, agentFailure(err)
	}
}

func (s *VirtualisService) InstanceVNC(ctx context.Context, id uint) (agentclient.VNCInfo, error) {
	instance, err := s.GetInstance(id)
	if err != nil {
		return agentclient.VNCInfo{}, err
	}
	if instance.Agent == nil {
		return agentclient.VNCInfo{}, Conflict("实例没有关联被控节点")
	}
	client, err := s.agentClient(instance.Agent)
	if err != nil {
		return agentclient.VNCInfo{}, err
	}
	vnc, err := client.VNC(ctx, toWireInstance(instance, instance.Image))
	if err != nil {
		return agentclient.VNCInfo{}, agentFailure(err)
	}
	return vnc, nil
}

// CreateVNCTicket 为实例签发一张一次性 VNC 短票，供机器对机器调用方
// （如 Levis 对接插件）转交给最终用户的浏览器建连。短票只活 120 秒，
// 首次建连即核销，主控重启后全部失效。
func (s *VirtualisService) CreateVNCTicket(ctx context.Context, id uint) (string, time.Time, error) {
	instance, err := s.GetInstance(id)
	if err != nil {
		return "", time.Time{}, err
	}
	if instance.Agent == nil {
		return "", time.Time{}, Conflict("实例没有关联被控节点")
	}
	// 先确认被控侧 VNC 可用，不可用就不发短票，调用方直接展示原因。
	client, err := s.agentClient(instance.Agent)
	if err != nil {
		return "", time.Time{}, err
	}
	vnc, err := client.VNC(ctx, toWireInstance(instance, instance.Image))
	if err != nil {
		return "", time.Time{}, agentFailure(err)
	}
	if !vnc.Available {
		if vnc.Message == "" {
			vnc.Message = "当前实例没有可用的 VNC"
		}
		return "", time.Time{}, Conflict("%s", vnc.Message)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	ticket := hex.EncodeToString(raw)
	expires := time.Now().Add(vncTicketTTL)
	s.vncMu.Lock()
	if s.vncTickets == nil {
		s.vncTickets = make(map[string]vncTicket)
	}
	// 顺手清理过期短票，map 不会无限增长。
	for k, v := range s.vncTickets {
		if time.Now().After(v.expires) {
			delete(s.vncTickets, k)
		}
	}
	s.vncTickets[ticket] = vncTicket{instanceID: id, expires: expires}
	s.vncMu.Unlock()
	return ticket, expires, nil
}

// ConsumeVNCTicket 核销短票：凭证匹配、未过期且绑定同一实例才放行，
// 通过即删除（一次性），失败不透露具体原因。
func (s *VirtualisService) ConsumeVNCTicket(ticket string, id uint) bool {
	if ticket == "" || id == 0 {
		return false
	}
	s.vncMu.Lock()
	defer s.vncMu.Unlock()
	v, ok := s.vncTickets[ticket]
	if !ok || v.instanceID != id || time.Now().After(v.expires) {
		return false
	}
	delete(s.vncTickets, ticket)
	return true
}
func (s *VirtualisService) agentClient(agent *model.Agent) (*agentclient.Client, error) {
	if agent == nil || strings.TrimSpace(agent.Endpoint) == "" {
		return nil, Conflict("被控节点没有可访问的 endpoint")
	}
	// Plaintext tokens live in the process-wide memory cache (populated at
	// create/rotate time and by every authenticated heartbeat), never in the
	// database — see AgentService.RPCToken for the cold-restart semantics.
	token, err := NewAgentService(s.db).RPCToken(agent.ID)
	if err != nil {
		return nil, err
	}
	client, err := agentclient.New(agent.Endpoint, token)
	if err != nil {
		return nil, agentFailure(err)
	}
	return client, nil
}

func agentFailure(err error) error {
	if err == nil {
		return nil
	}
	return Unavailable("被控节点操作失败: %s", err.Error())
}

// Images

func (s *VirtualisService) ListImages() ([]model.Image, error) {
	var items []model.Image
	if err := s.db.Order("id DESC").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

func (s *VirtualisService) GetImage(id uint) (*model.Image, error) {
	var image model.Image
	if err := s.db.First(&image, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, NotFound("image not found")
		}
		return nil, err
	}
	return &image, nil
}

type CreateImageRequest struct {
	Name         string `json:"name"`
	Driver       string `json:"driver"`
	Type         string `json:"type"`
	OSType       string `json:"os_type"`
	OSVersion    string `json:"os_version"`
	Arch         string `json:"arch"`
	OriginalName string `json:"original_name"`
	MimeType     string `json:"mime_type"`
	FilePath     string `json:"file_path"`
	Size         int64  `json:"size"`
	SizeBytes    int64  `json:"size_bytes"`
	Checksum     string `json:"checksum"`
}

func (s *VirtualisService) CreateImage(req CreateImageRequest) (*model.Image, error) {
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return nil, BadRequest("image name required")
	}
	if len(req.Name) > 128 {
		return nil, BadRequest("image name too long")
	}
	driverName, err := normalizeImageDriver(req.Driver)
	if err != nil {
		return nil, err
	}
	kind := normalizeImageType(req.Type, req.OriginalName, req.FilePath)
	filePath := strings.TrimSpace(req.FilePath)
	if filePath == "" {
		return nil, BadRequest("file_path required")
	}
	size := req.SizeBytes
	if size == 0 {
		size = req.Size
	}
	image := model.Image{
		Name:         req.Name,
		Driver:       driverName,
		Type:         kind,
		OSType:       strings.TrimSpace(req.OSType),
		OSVersion:    strings.TrimSpace(req.OSVersion),
		Arch:         strings.TrimSpace(req.Arch),
		OriginalName: strings.TrimSpace(req.OriginalName),
		MimeType:     strings.TrimSpace(req.MimeType),
		FilePath:     filePath,
		SizeBytes:    size,
		Checksum:     strings.TrimSpace(req.Checksum),
		Status:       model.ImageStatusAvailable,
		IsPublic:     true,
	}
	if err := s.db.Create(&image).Error; err != nil {
		return nil, err
	}
	return &image, nil
}

type UploadImageRequest struct {
	Name      string
	Driver    string
	Type      string
	OSType    string
	OSVersion string
	Arch      string
}

func (s *VirtualisService) UploadImage(req UploadImageRequest, filename string, r io.Reader) (*model.Image, error) {
	if s.storage == nil {
		return nil, Internal("image storage unavailable")
	}
	filename = strings.TrimSpace(filename)
	if filename == "" {
		return nil, BadRequest("请选择镜像文件")
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		req.Name = filename
	}
	driverName, err := normalizeImageDriver(req.Driver)
	if err != nil {
		return nil, err
	}
	kind := normalizeImageType(req.Type, filename, "")
	if kind == model.ImageTypeISO && driverName == model.DriverIncus {
		return nil, BadRequest("容器不支持 ISO 镜像")
	}
	filePath, size, mimeType, checksum, err := s.storage.SaveNamed("uploads", filename, r, maxImageUploadSize)
	if err != nil {
		return nil, BadRequest("保存镜像失败: %s", err.Error())
	}
	image := &model.Image{
		Name:         req.Name,
		Driver:       driverName,
		Type:         kind,
		OSType:       strings.TrimSpace(req.OSType),
		OSVersion:    strings.TrimSpace(req.OSVersion),
		Arch:         strings.TrimSpace(req.Arch),
		OriginalName: filename,
		MimeType:     mimeType,
		FilePath:     filePath,
		SizeBytes:    size,
		Checksum:     checksum,
		Status:       model.ImageStatusAvailable,
		IsPublic:     true,
	}
	if err := s.db.Create(image).Error; err != nil {
		_ = s.storage.Remove(filePath)
		return nil, err
	}
	return image, nil
}

func normalizeImageDriver(name string) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return "", BadRequest("请选择镜像驱动")
	}
	if !model.ValidDriver(name) {
		return "", BadRequest("invalid driver %q", name)
	}
	return name, nil
}

func normalizeImageType(kind, filename, filePath string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == model.ImageTypeISO {
		return model.ImageTypeISO
	}
	if kind == model.ImageTypeDisk {
		return model.ImageTypeDisk
	}
	lower := strings.ToLower(filename + " " + filePath)
	if strings.HasSuffix(lower, ".iso") {
		return model.ImageTypeISO
	}
	return model.ImageTypeDisk
}

func (s *VirtualisService) DeleteImage(id uint) error {
	var count int64
	if err := s.db.Model(&model.Instance{}).Where("image_id = ?", id).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return Conflict("image is in use by %d instances", count)
	}
	image, err := s.GetImage(id)
	if err != nil {
		return err
	}
	if err := s.db.Delete(&model.Image{}, id).Error; err != nil {
		return err
	}
	if s.storage != nil && image.FilePath != "" {
		if err := s.storage.Remove(image.FilePath); err != nil {
			return err
		}
	}
	return nil
}

func (s *VirtualisService) EnsureDefaultImages() error {
	// 镜像必须通过上传离线存储到 data/images 目录，不再创建指向不存在文件的在线占位镜像
	// 保留此方法以兼容旧数据库的调用点，但不再自动插入默认镜像记录
	return nil
}

func (s *VirtualisService) openImage(image *model.Image) (io.ReadCloser, string, error) {
	if image == nil || image.FilePath == "" || s.storage == nil {
		return nil, "", nil
	}
	f, err := s.storage.Open(image.FilePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && image.OriginalName == "" {
			return nil, "", nil
		}
		return nil, "", BadRequest("镜像文件不可读取: %s", err.Error())
	}
	name := image.OriginalName
	if name == "" {
		name = image.Name
	}
	return f, name, nil
}

// openExtraImage 打开 Incus 分割镜像的 meta 文件（无则返回 nil）。
func (s *VirtualisService) openExtraImage(image *model.Image) (io.ReadCloser, string, error) {
	if image == nil || image.ExtraPath == "" || s.storage == nil {
		return nil, "", nil
	}
	f, err := s.storage.Open(image.ExtraPath)
	if err != nil {
		return nil, "", BadRequest("镜像 meta 文件不可读取: %s", err.Error())
	}
	return f, "meta.tar.xz", nil
}

func toWireImage(image *model.Image) *agentclient.Image {
	if image == nil {
		return nil
	}
	return &agentclient.Image{
		ID:           image.ID,
		Name:         image.Name,
		DisplayName:  image.DisplayName,
		Driver:       image.Driver,
		Type:         image.Type,
		OriginalName: image.OriginalName,
		SizeBytes:    image.SizeBytes,
		Checksum:     image.Checksum,
		ExtraPath:    image.ExtraPath,
	}
}

func toWireInstance(instance *model.Instance, image *model.Image) agentclient.Instance {
	rules, policy := effectiveFirewall(instance)
	// 旧 wire 包含禁用规则（被控跳过）；保留该可观察兼容性。
	if policy == nil {
		rules = toWireFirewall(instance.FirewallRules)
	}
	return agentclient.Instance{
		ID:             instance.ID,
		Name:           instance.Name,
		DisplayName:    instance.DisplayName,
		Driver:         instance.Driver,
		Type:           instance.Type,
		Status:         instance.Status,
		ImageID:        instance.ImageID,
		ObservedIP:     instance.ObservedIP,
		Spec:           instance.Spec,
		Network:        instance.Network,
		Image:          toWireImage(image),
		NATMappings:    toWireMappings(instance.NATMappings),
		Firewall:       rules,
		FirewallPolicy: policy,
	}
}

func toWireMappings(items []model.NATMapping) []agentclient.NATMapping {
	out := make([]agentclient.NATMapping, 0, len(items))
	for _, item := range items {
		out = append(out, agentclient.NATMapping{
			Protocol:  item.Protocol,
			HostPort:  item.HostPort,
			GuestPort: item.GuestPort,
		})
	}
	return out
}

// mergeRemoteNetwork 把被控回填的 NAT 地址/MAC 合并进实例。
//
// NAT 模式的 MAC 与 IPv4 是被控派生并维护的系统数据（静态保留 IP、按域里
// 真实网卡对账后的结果），被控上报的非空值是权威值，直接采纳——否则历史
// 实例的陈旧记录永远得不到纠正；被控未上报时保留主控已有值。独立 IP 模式
// 的字段是用户声明，仍只在为空时填充。
func mergeRemoteNetwork(instance *model.Instance, remote agentclient.Instance) {
	instance.ObservedIP = validObservedIPv4(remote.ObservedIP)
	if instance.ObservedIP != "" {
		instance.IP = instance.ObservedIP
	}
	if instance.Network.Mode != model.NetworkModeNAT {
		return
	}
	if remote.Network.IPv4 != "" {
		instance.Network.IPv4 = remote.Network.IPv4
	}
	if remote.Network.MAC != "" {
		instance.Network.MAC = remote.Network.MAC
	}
}

func firstAgentNetworkIPv4(network agentclient.NetworkStatus) string {
	for _, iface := range network.Interfaces {
		if iface.Name == "lo" {
			continue
		}
		for _, addr := range iface.IPv4 {
			ip := strings.Split(addr, "/")[0]
			if net.ParseIP(ip) != nil {
				return ip
			}
		}
	}
	return ""
}

// primaryConfiguredIP returns Network.IPv4 without CIDR for compatibility with
// the legacy top-level Instance.IP field.
func primaryConfiguredIP(network model.NetworkConfig) string {
	return strings.Split(strings.TrimSpace(network.IPv4), "/")[0]
}

// ConfigureInstanceNetwork validates/persists the desired network and asks the
// agent to synchronously re-run IPv4, SSH and NAT reconciliation.
func (s *VirtualisService) ConfigureInstanceNetwork(ctx context.Context, id uint, desired model.NetworkConfig) (result *model.Instance, operationID string, err error) {
	guard, err := s.beginOperation(ctx, id, "configure_network")
	if err != nil {
		return nil, "", err
	}
	defer guard.finishInstance(&err, &result)
	operationID = guard.token
	appendOperationLog(s.db, id, operationID, model.OperationConfigureNetwork, "start", model.OperationRunning, "开始配置实例网络", nil)
	instance, err := s.GetInstance(id)
	if err != nil {
		appendOperationLog(s.db, id, operationID, model.OperationConfigureNetwork, "load", model.OperationFailed, "读取实例失败", err)
		return nil, operationID, err
	}
	if instance.Agent == nil {
		err = Conflict("实例没有关联被控节点")
		appendOperationLog(s.db, id, operationID, model.OperationConfigureNetwork, "validate", model.OperationFailed, "实例未关联被控节点", err)
		return nil, operationID, err
	}
	if strings.TrimSpace(desired.Mode) == "" {
		desired = instance.Network
	}
	desired, err = model.NormalizeNetworkConfig(desired)
	if err != nil {
		err = BadRequest("%s", err.Error())
		appendOperationLog(s.db, id, operationID, model.OperationConfigureNetwork, "validate", model.OperationFailed, "网络参数校验失败", err)
		return nil, operationID, err
	}
	if desired.Mode == model.NetworkModeDedicated {
		client, clientErr := s.agentClient(instance.Agent)
		if clientErr != nil {
			return nil, operationID, clientErr
		}
		summary, hostErr := client.HostNetwork(ctx)
		if hostErr != nil {
			err = agentFailure(hostErr)
			appendOperationLog(s.db, id, operationID, model.OperationConfigureNetwork, "validate", model.OperationFailed, "读取被控网卡失败", err)
			return nil, operationID, err
		}
		if err = validateDedicatedHost(desired, summary); err != nil {
			appendOperationLog(s.db, id, operationID, model.OperationConfigureNetwork, "validate", model.OperationFailed, "独立 IP 条件不足", err)
			return nil, operationID, err
		}
		if desired.IPv4 != "" {
			taken, takenErr := s.dedicatedIPTaken(instance.Agent.ID, desired.IPv4, id)
			if takenErr != nil {
				return nil, operationID, takenErr
			}
			if taken {
				err = Conflict("独立 IP %s 已被其它实例占用", desired.IPv4)
				appendOperationLog(s.db, id, operationID, model.OperationConfigureNetwork, "validate", model.OperationFailed, "独立 IP 冲突", err)
				return nil, operationID, err
			}
		}
	}
	instance.Network = desired
	client, err := s.agentClient(instance.Agent)
	if err != nil {
		appendOperationLog(s.db, id, operationID, model.OperationConfigureNetwork, "connect", model.OperationFailed, "连接被控失败", err)
		return nil, operationID, err
	}
	appendOperationLog(s.db, id, operationID, model.OperationConfigureNetwork, "agent", model.OperationRunning, "被控开始配置 IPv4、SSH 和 NAT", nil)
	remote, observedIP, err := client.ConfigureNetwork(ctx, toWireInstance(instance, instance.Image), desired, instance.LoadSSHPassword())
	if err != nil {
		wrapped := agentFailure(err)
		_ = s.db.Model(instance).Updates(map[string]any{"network_error": err.Error(), "ssh_ready": false}).Error
		appendOperationLog(s.db, id, operationID, model.OperationConfigureNetwork, "agent", model.OperationFailed, "被控配置网络失败", err)
		return nil, operationID, wrapped
	}
	applyWireInstance(instance, remote)
	mergeRemoteNetwork(instance, remote)
	if observedIP != "" {
		instance.ObservedIP = observedIP
		instance.IP = observedIP
	}
	instance.SSHReady = true
	instance.NetworkError = ""
	networkJSON, err := json.Marshal(instance.Network)
	if err != nil {
		return nil, operationID, err
	}
	updates := map[string]any{
		"status": instance.Status, "driver": instance.Driver, "network": string(networkJSON),
		"ip": instance.IP, "observed_ip": instance.ObservedIP, "ssh_ready": true, "network_error": "",
	}
	if err := s.db.Model(instance).Updates(updates).Error; err != nil {
		appendOperationLog(s.db, id, operationID, model.OperationConfigureNetwork, "persist", model.OperationFailed, "保存网络状态失败", err)
		return nil, operationID, err
	}
	appendOperationLog(s.db, id, operationID, model.OperationConfigureNetwork, "complete", model.OperationSuccess, fmt.Sprintf("网络配置完成，IPv4 %s", instance.IP), nil)
	result, err = s.GetInstance(id)
	return result, operationID, err
}

// GeneratePassword 生成 n 位字母数字随机密码（去掉了易混字符）。
func GeneratePassword(n int) string {
	const charset = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	raw := make([]byte, n)
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand 失败属于系统级异常，直接降级为确定性占位。
		for i := range raw {
			raw[i] = charset[i%len(charset)]
		}
		return string(raw)
	}
	for i, b := range buf {
		raw[i] = charset[int(b)%len(charset)]
	}
	return string(raw)
}

// allocateNATPort 在自动分配范围内为被控找一个未被占用的宿主端口。
func (s *VirtualisService) allocateNATPort(agentID uint) (int, error) {
	var taken []int
	if err := s.db.Model(&model.NATMapping{}).Where("agent_id = ?", agentID).Pluck("host_port", &taken).Error; err != nil {
		return 0, err
	}
	used := make(map[int]bool, len(taken))
	for _, port := range taken {
		used[port] = true
	}
	for port := model.NATPortMin; port <= model.NATPortMax; port++ {
		if !used[port] {
			return port, nil
		}
	}
	return 0, Conflict("NAT 宿主端口已耗尽（%d-%d）", model.NATPortMin, model.NATPortMax)
}

// CreateNATMappingRequest 是新增 NAT 映射的入参；HostPort 为 0 时自动分配。
type CreateNATMappingRequest struct {
	Protocol  string `json:"protocol"`
	HostPort  int    `json:"host_port"`
	GuestPort int    `json:"guest_port"`
	Remark    string `json:"remark"`
}

// CreateNATMapping 为实例添加 NAT 端口转发；实例运行中时即时下发被控。
func (s *VirtualisService) CreateNATMapping(ctx context.Context, instanceID uint, req CreateNATMappingRequest) (result *model.NATMapping, err error) {
	guard, err := s.beginOperation(ctx, instanceID, "nat_create")
	if err != nil {
		return nil, err
	}
	defer guard.finish(&err)
	instance, err := s.GetInstance(instanceID)
	if err != nil {
		return nil, err
	}
	if instance.AgentID == nil || *instance.AgentID == 0 {
		return nil, Conflict("实例没有关联被控节点")
	}
	if instance.MaxNATMappings > 0 {
		var count int64
		if err := s.db.Model(&model.NATMapping{}).Where("instance_id = ?", instanceID).Count(&count).Error; err != nil {
			return nil, err
		}
		if int(count) >= instance.MaxNATMappings {
			return nil, Conflict("已达该实例的 NAT 映射上限（%d 条）", instance.MaxNATMappings)
		}
	}
	protocolName := strings.ToLower(strings.TrimSpace(req.Protocol))
	if protocolName == "" {
		protocolName = "tcp"
	}
	if !model.ValidNATProtocol(protocolName) {
		return nil, BadRequest("协议只支持 tcp/udp")
	}
	if req.GuestPort < 1 || req.GuestPort > 65535 {
		return nil, BadRequest("实例端口需在 1-65535 之间")
	}
	hostPort := req.HostPort
	if hostPort == 0 {
		if hostPort, err = s.allocateNATPort(*instance.AgentID); err != nil {
			return nil, err
		}
	}
	if hostPort < 1 || hostPort > 65535 {
		return nil, BadRequest("宿主端口需在 1-65535 之间")
	}
	// 同一被控上宿主端口不能重复（不同实例之间也不行）。
	var dup int64
	if err := s.db.Model(&model.NATMapping{}).
		Where("agent_id = ? AND protocol = ? AND host_port = ?", *instance.AgentID, protocolName, hostPort).
		Count(&dup).Error; err != nil {
		return nil, err
	}
	if dup > 0 {
		return nil, Conflict("宿主端口 %d/%s 已被其它映射占用", hostPort, protocolName)
	}
	mapping := model.NATMapping{
		InstanceID: instanceID,
		AgentID:    *instance.AgentID,
		Protocol:   protocolName,
		HostPort:   hostPort,
		GuestPort:  req.GuestPort,
		Remark:     strings.TrimSpace(req.Remark),
	}
	if err := s.db.Create(&mapping).Error; err != nil {
		return nil, err
	}
	// 预载清单还是旧值，先补上新映射再下发。
	instance.NATMappings = append(instance.NATMappings, mapping)
	return &mapping, s.syncNATIfRunning(ctx, instance)
}

// DeleteNATMapping 删除 NAT 映射；运行中的实例即时撤销对应规则。
func (s *VirtualisService) DeleteNATMapping(ctx context.Context, instanceID, mappingID uint) (err error) {
	guard, err := s.beginOperation(ctx, instanceID, "nat_delete")
	if err != nil {
		return err
	}
	defer guard.finish(&err)
	instance, err := s.GetInstance(instanceID)
	if err != nil {
		return err
	}
	result := s.db.Where("instance_id = ? AND id = ?", instanceID, mappingID).Delete(&model.NATMapping{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return NotFound("映射不存在")
	}
	kept := instance.NATMappings[:0]
	for _, item := range instance.NATMappings {
		if item.ID != mappingID {
			kept = append(kept, item)
		}
	}
	instance.NATMappings = kept
	return s.syncNATIfRunning(ctx, instance)
}

// syncNATIfRunning 把最新的映射清单推给被控；实例未运行时被控侧没有
// 规则可对账，直接跳过。
func (s *VirtualisService) syncNATIfRunning(ctx context.Context, instance *model.Instance) error {
	if instance.Status != model.InstanceStatusRunning || instance.Agent == nil {
		return nil
	}
	client, err := s.agentClient(instance.Agent)
	if err != nil {
		return err
	}
	if err = client.ApplyNAT(ctx, toWireInstance(instance, instance.Image)); err != nil {
		return Conflict("NAT desired state saved but Agent sync failed: %s", err)
	}
	return nil
}

// SetInstancePassword 设置实例的 root 密码并落库；实例运行中时异步推给
// 被控注入（QEMU 依赖 guest agent，注入可能滞后于本调用返回）。
func (s *VirtualisService) SetInstancePassword(ctx context.Context, instanceID uint, password string) (result *model.Instance, err error) {
	guard, err := s.beginOperation(ctx, instanceID, "password")
	if err != nil {
		return nil, err
	}
	defer guard.finishInstance(&err, &result)
	password = strings.TrimSpace(password)
	if utf8.RuneCountInString(password) < 6 || utf8.RuneCountInString(password) > 64 {
		return nil, BadRequest("密码长度需在 6-64 个字符之间")
	}
	instance, err := s.GetInstance(instanceID)
	if err != nil {
		return nil, err
	}
	instance.StoreSSHPassword(password)
	if err := s.db.Model(instance).Update("config_json", instance.ConfigJSON).Error; err != nil {
		return nil, err
	}
	if instance.Status == model.InstanceStatusRunning && instance.Agent != nil {
		if client, err := s.agentClient(instance.Agent); err == nil {
			// 后台注入：QEMU 要等 guest agent 就绪，不阻塞本次请求。
			wire := toWireInstance(instance, instance.Image)
			go func() {
				applyCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				if err := client.SetRootPassword(applyCtx, wire, password); err != nil {
					log.Printf("实例 %d 注入密码失败: %v", instanceID, err)
				}
			}()
		}
	}
	return s.GetInstance(instanceID)
}

func applyWireInstance(instance *model.Instance, remote agentclient.Instance) {
	if remote.Driver != "" {
		instance.Driver = remote.Driver
	}
	if remote.Status != "" {
		instance.Status = remote.Status
	}
}
