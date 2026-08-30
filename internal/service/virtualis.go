package service

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"strings"
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
}

// NewVirtualisService constructs the orchestration service. All driver
// operations run on agents; the master only coordinates and persists.
func NewVirtualisService(db *gorm.DB, stores ...*storage.Store) *VirtualisService {
	var store *storage.Store
	if len(stores) > 0 {
		store = stores[0]
	}
	return &VirtualisService{db: db, settings: NewSettingService(db), storage: store}
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
			available := false
			for _, ok := range summary {
				available = available || ok
			}
			items = append(items, DriverStatus{Name: name, Available: available})
			continue
		}
		items = append(items, DriverStatus{Name: name, Available: summary[name]})
	}
	_ = ctx
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
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	var total int64
	if err := s.db.Model(&model.Instance{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []model.Instance
	offset := (page - 1) * pageSize
	if err := s.db.Preload("Image").Preload("Agent").Order("id DESC").Offset(offset).Limit(pageSize).Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// GetInstance returns instance by id.
func (s *VirtualisService) GetInstance(id uint) (*model.Instance, error) {
	var inst model.Instance
	if err := s.db.Preload("Image").Preload("Agent").Preload("NATMappings").First(&inst, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, NotFound("instance not found")
		}
		return nil, err
	}
	inst.SSHPassword = inst.LoadSSHPassword()
	return &inst, nil
}

type CreateInstanceRequest struct {
	Name    string              `json:"name"`
	Driver  string              `json:"driver"`
	Type    string              `json:"type"`
	Spec    model.InstanceSpec  `json:"spec"`
	Network model.NetworkConfig `json:"network"`
	ImageID *uint               `json:"image_id"`
	AgentID *uint               `json:"agent_id"`
	// MaxNATMappings 是允许创建的 NAT 映射上限，0 表示不限。
	MaxNATMappings int `json:"max_nat_mappings"`
	// AutoPassword 为 true（默认）时生成随机 root 密码并存库供管理页查看。
	AutoPassword *bool `json:"auto_password"`
}

// CreateInstance records an instance and provisions it on the selected agent.
func (s *VirtualisService) CreateInstance(ctx context.Context, req CreateInstanceRequest) (*model.Instance, error) {
	if err := ValidateInstanceName(req.Name); err != nil {
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

	def := s.settings.Virtualis()
	spec := req.Spec
	if spec.CPU == 0 {
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
	network, err := model.NormalizeNetworkConfig(req.Network)
	if err != nil {
		return nil, BadRequest("%s", err.Error())
	}
	// 独立 IP 模式的两个闸门都在这里拦：
	// 1) 主机必须有至少 2 个 IPv4 地址（一个归主机，才有富余给实例网段）；
	// 2) 同一被控上不允许两个实例声明同一个独立 IP。
	if network.Mode == model.NetworkModeDedicated {
		summary, hnErr := client.HostNetwork(ctx)
		if hnErr != nil {
			return nil, agentFailure(hnErr)
		}
		if summary.IPv4Count < 2 {
			return nil, BadRequest("独立 IP 模式要求被控主机拥有至少 2 个 IPv4 地址，当前仅 %d 个", summary.IPv4Count)
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
	if err := s.db.Create(instance).Error; err != nil {
		return nil, err
	}
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
	remote, remoteErr := client.CreateInstance(ctx, wireInstance, toWireImage(image), reader, filename)
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
		"status": instance.Status, "driver": instance.Driver, "network": string(networkJSON),
	}).Error; err != nil {
		return nil, err
	}
	return s.GetInstance(instance.ID)
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

// DeleteInstance asks the assigned agent to remove the runtime before deleting
// the master record. Legacy records without an agent are only removed from DB.
func (s *VirtualisService) DeleteInstance(ctx context.Context, id uint) error {
	instance, err := s.GetInstance(id)
	if err != nil {
		return err
	}
	if instance.AgentID != nil && instance.Agent != nil {
		client, clientErr := s.agentClient(instance.Agent)
		if clientErr != nil {
			return clientErr
		}
		if err := client.DeleteInstance(ctx, toWireInstance(instance, instance.Image)); err != nil {
			return agentFailure(err)
		}
	}
	return s.db.Delete(&model.Instance{}, id).Error
}

func (s *VirtualisService) PowerInstance(ctx context.Context, id uint, action string) (*model.Instance, error) {
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
	client, err := s.agentClient(instance.Agent)
	if err != nil {
		return nil, err
	}
	var reader io.ReadCloser
	var filename string
	if action == model.ActionReinstall {
		reader, filename, err = s.openImage(instance.Image)
		if err != nil {
			return nil, err
		}
		if reader != nil {
			defer reader.Close()
		}
	}
	remote, err := client.PowerInstance(ctx, toWireInstance(instance, instance.Image), action, toWireImage(instance.Image), reader, filename)
	if err != nil {
		_ = s.db.Model(instance).Update("status", model.InstanceStatusError).Error
		return nil, agentFailure(err)
	}
	applyWireInstance(instance, remote)
	mergeRemoteNetwork(instance, remote)
	// map 形式的 Updates 不触发字段上的 JSON 序列化器，网络配置先自行序列化。
	networkJSON, mErr := json.Marshal(instance.Network)
	if mErr != nil {
		return nil, mErr
	}
	if err := s.db.Model(instance).Updates(map[string]any{"status": instance.Status, "driver": instance.Driver, "network": string(networkJSON)}).Error; err != nil {
		return nil, err
	}
	return s.GetInstance(instance.ID)
}

func (s *VirtualisService) RefreshStatus(ctx context.Context, id uint) (*model.Instance, error) {
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
	if err := s.db.Model(instance).Updates(map[string]any{"status": instance.Status, "driver": instance.Driver, "network": string(networkJSON)}).Error; err != nil {
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

func (s *VirtualisService) InstanceNetwork(ctx context.Context, id uint) (agentclient.NetworkStatus, error) {
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
	network, err := client.Network(ctx, toWireInstance(instance, instance.Image))
	if err != nil {
		return agentclient.NetworkStatus{}, agentFailure(err)
	}
	return network, nil
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

func (s *VirtualisService) agentClient(agent *model.Agent) (*agentclient.Client, error) {
	if agent == nil || strings.TrimSpace(agent.Endpoint) == "" {
		return nil, Conflict("被控节点没有可访问的 endpoint")
	}
	if strings.TrimSpace(agent.Token) == "" {
		return nil, Conflict("被控 token 已失效，请删除节点后重新添加")
	}
	client, err := agentclient.New(agent.Endpoint, agent.Token)
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
	}
}

func toWireInstance(instance *model.Instance, image *model.Image) agentclient.Instance {
	return agentclient.Instance{
		ID:          instance.ID,
		Name:        instance.Name,
		DisplayName: instance.DisplayName,
		Driver:      instance.Driver,
		Type:        instance.Type,
		Status:      instance.Status,
		ImageID:     instance.ImageID,
		Spec:        instance.Spec,
		Network:     instance.Network,
		Image:       toWireImage(image),
		NATMappings: toWireMappings(instance.NATMappings),
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
func (s *VirtualisService) CreateNATMapping(ctx context.Context, instanceID uint, req CreateNATMappingRequest) (*model.NATMapping, error) {
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
	s.syncNATIfRunning(ctx, instance)
	return &mapping, nil
}

// DeleteNATMapping 删除 NAT 映射；运行中的实例即时撤销对应规则。
func (s *VirtualisService) DeleteNATMapping(ctx context.Context, instanceID, mappingID uint) error {
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
	s.syncNATIfRunning(ctx, instance)
	return nil
}

// syncNATIfRunning 把最新的映射清单推给被控；实例未运行时被控侧没有
// 规则可对账，直接跳过。
func (s *VirtualisService) syncNATIfRunning(ctx context.Context, instance *model.Instance) {
	if instance.Status != model.InstanceStatusRunning || instance.Agent == nil {
		return
	}
	client, err := s.agentClient(instance.Agent)
	if err != nil {
		return
	}
	_ = client.ApplyNAT(ctx, toWireInstance(instance, instance.Image))
}

// SetInstancePassword 设置实例的 root 密码并落库；实例运行中时异步推给
// 被控注入（QEMU 依赖 guest agent，注入可能滞后于本调用返回）。
func (s *VirtualisService) SetInstancePassword(ctx context.Context, instanceID uint, password string) (*model.Instance, error) {
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
