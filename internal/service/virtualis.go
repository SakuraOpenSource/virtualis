package service

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"

	"gorm.io/gorm"

	"github.com/SakuraOpenSource/virtualis/internal/agentclient"
	"github.com/SakuraOpenSource/virtualis/internal/driver"
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

// NewVirtualisService keeps the old registry argument for source compatibility;
// a master registry is intentionally not used for instance operations.
func NewVirtualisService(db *gorm.DB, _ *driver.Registry, stores ...*storage.Store) *VirtualisService {
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
	if err := s.db.Preload("Image").Preload("Agent").First(&inst, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, NotFound("instance not found")
		}
		return nil, err
	}
	return &inst, nil
}

type CreateInstanceRequest struct {
	Name    string             `json:"name"`
	Driver  string             `json:"driver"`
	Type    string             `json:"type"`
	Spec    model.InstanceSpec `json:"spec"`
	ImageID *uint              `json:"image_id"`
	AgentID *uint              `json:"agent_id"`
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
		return nil, err
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
		Name:    strings.TrimSpace(req.Name),
		Driver:  driverName,
		Type:    normalizeInstanceType(req.Type),
		Spec:    spec,
		Status:  model.InstanceStatusCreating,
		ImageID: req.ImageID,
		AgentID: req.AgentID,
	}
	if err := s.db.Create(instance).Error; err != nil {
		return nil, err
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
	remote, remoteErr := client.CreateInstance(ctx, toWireInstance(instance, image), toWireImage(image), reader, filename)
	if remoteErr != nil {
		_ = s.db.Model(instance).Update("status", model.InstanceStatusError).Error
		instance.Status = model.InstanceStatusError
		return instance, remoteErr
	}
	applyWireInstance(instance, remote)
	if !model.ValidInstanceStatus(instance.Status) || instance.Status == model.InstanceStatusCreating {
		instance.Status = model.InstanceStatusStopped
	}
	if err := s.db.Model(instance).Updates(map[string]any{"status": instance.Status, "driver": instance.Driver}).Error; err != nil {
		return nil, err
	}
	return s.GetInstance(instance.ID)
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
			return err
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
		return nil, err
	}
	applyWireInstance(instance, remote)
	if err := s.db.Model(instance).Updates(map[string]any{"status": instance.Status, "driver": instance.Driver}).Error; err != nil {
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
		return nil, err
	}
	applyWireInstance(instance, remote)
	if !model.ValidInstanceStatus(instance.Status) {
		return nil, BadRequest("被控返回了无效实例状态")
	}
	if err := s.db.Model(instance).Updates(map[string]any{"status": instance.Status, "driver": instance.Driver}).Error; err != nil {
		return nil, err
	}
	return s.GetInstance(id)
}

func (s *VirtualisService) agentClient(agent *model.Agent) (*agentclient.Client, error) {
	if agent == nil || strings.TrimSpace(agent.Endpoint) == "" {
		return nil, Conflict("被控节点没有可访问的 endpoint")
	}
	if strings.TrimSpace(agent.Token) == "" {
		return nil, Conflict("被控 token 已失效，请删除节点后重新添加")
	}
	return agentclient.New(agent.Endpoint, agent.Token)
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
	if kind == model.ImageTypeISO && driverName == model.DriverLXC {
		return nil, BadRequest("LXC 不支持 ISO 镜像")
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
	defaults := []CreateImageRequest{
		{Name: "ubuntu-22.04", Driver: model.DriverQEMU, Type: model.ImageTypeDisk, FilePath: "images/ubuntu-22.04.qcow2"},
		{Name: "debian-12", Driver: model.DriverQEMU, Type: model.ImageTypeDisk, FilePath: "images/debian-12.qcow2"},
		{Name: "alpine-3.19", Driver: model.DriverLXC, Type: model.ImageTypeDisk, FilePath: "images/alpine-3.19.tar.gz"},
	}
	for _, item := range defaults {
		var count int64
		if err := s.db.Model(&model.Image{}).Where("name = ?", item.Name).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			if _, err := s.CreateImage(item); err != nil {
				return err
			}
		}
	}
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
		Image:       toWireImage(image),
	}
}

func applyWireInstance(instance *model.Instance, remote agentclient.Instance) {
	if remote.Driver != "" {
		instance.Driver = remote.Driver
	}
	if remote.Status != "" {
		instance.Status = remote.Status
	}
}
