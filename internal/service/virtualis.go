package service

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/SakuraOpenSource/virtualis/internal/driver"
	"github.com/SakuraOpenSource/virtualis/internal/model"
)

// VirtualisService manages instances and images together with driver orchestration.
type VirtualisService struct {
	db       *gorm.DB
	registry *driver.Registry
	settings *SettingService
}

// NewVirtualisService creates a VirtualisService.
// If registry is nil, DefaultRegistry() is used.
func NewVirtualisService(db *gorm.DB, reg *driver.Registry) *VirtualisService {
	if reg == nil {
		reg = driver.DefaultRegistry()
	}
	return &VirtualisService{
		db:       db,
		registry: reg,
		settings: NewSettingService(db),
	}
}

// ListDrivers returns available driver names and probe status.
func (s *VirtualisService) ListDrivers(ctx context.Context) map[string]error {
	return s.registry.ProbeAll(ctx)
}

// DriverNames returns sorted list of driver names.
func (s *VirtualisService) DriverNames() []string {
	return s.registry.List()
}

// Instance CRUD + power

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
	if err := s.db.Preload("Image").Order("id DESC").Offset(offset).Limit(pageSize).Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// GetInstance returns instance by id.
func (s *VirtualisService) GetInstance(id uint) (*model.Instance, error) {
	var inst model.Instance
	if err := s.db.Preload("Image").First(&inst, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, NotFound("instance not found")
		}
		return nil, err
	}
	return &inst, nil
}

// CreateInstanceRequest is input for creation.
type CreateInstanceRequest struct {
	Name   string           `json:"name"`
	Driver string           `json:"driver"`
	Spec   model.InstanceSpec `json:"spec"`
	ImageID *uint           `json:"image_id"`
}

// CreateInstance validates, persists and provisions via driver.
func (s *VirtualisService) CreateInstance(ctx context.Context, req CreateInstanceRequest) (*model.Instance, error) {
	if err := ValidateInstanceName(req.Name); err != nil {
		return nil, err
	}
	req.Driver = strings.TrimSpace(strings.ToLower(req.Driver))
	if req.Driver == "" {
		req.Driver = s.settings.Virtualis().DefaultDriver
	}
	if req.Driver != "mock" && !model.ValidDriver(req.Driver) && req.Driver != "incus" {
		return nil, BadRequest("invalid driver %q", req.Driver)
	}
	drv, ok := s.registry.Get(req.Driver)
	if !ok {
		drv2, err2 := s.registry.Resolve(ctx, req.Driver)
		if err2 != nil {
			return nil, BadRequest("driver %q not available", req.Driver)
		}
		drv = drv2
		req.Driver = drv.Name()
	}
	// Normalize spec with defaults
	def := s.settings.Virtualis()
	spec, err := model.NormalizeInstanceSpec(req.Spec)
	if err != nil {
		return nil, BadRequest("%s", err.Error())
	}
	if req.Spec.CPU == 0 {
		spec.CPU = def.DefaultCPU
	}
	if req.Spec.MemoryMB == 0 {
		spec.MemoryMB = def.DefaultMemory
	}
	if req.Spec.DiskGB == 0 {
		spec.DiskGB = def.DefaultDisk
	}
	spec, err = model.NormalizeInstanceSpec(spec)
	if err != nil {
		return nil, BadRequest("%s", err.Error())
	}

	var image *model.Image
	if req.ImageID != nil {
		img, err := s.GetImage(*req.ImageID)
		if err != nil {
			return nil, err
		}
		image = img
	}

	inst := model.Instance{
		Name:   strings.TrimSpace(req.Name),
		Driver: req.Driver,
		Spec:   spec,
		Status: model.InstanceStatusCreating,
		ImageID: req.ImageID,
	}
	if err := s.db.Create(&inst).Error; err != nil {
		return nil, err
	}

	// Provision via driver, fallback to mock on failure? We attempt hard.
	ctx2, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := drv.Create(ctx2, &inst); err != nil {
		_ = s.db.Model(&inst).Update("status", model.InstanceStatusError).Error
		inst.Status = model.InstanceStatusError
		return &inst, err
	}
	// Mark running or stopped; mock returns stopped, drivers may vary.
	inst.Status = model.InstanceStatusStopped
	// Try to start automatically? For now set stopped, let user start.
	_ = s.db.Model(&inst).Updates(map[string]any{"status": inst.Status, "driver": inst.Driver}).Error
	if image != nil {
		_ = s.db.Preload("Image").First(&inst, inst.ID).Error
	}
	return &inst, nil
}

// DeleteInstance removes instance both from driver and DB.
func (s *VirtualisService) DeleteInstance(ctx context.Context, id uint) error {
	inst, err := s.GetInstance(id)
	if err != nil {
		return err
	}
	drv, ok := s.registry.Get(inst.Driver)
	if !ok {
		// If driver missing, still delete DB entry.
		return s.db.Delete(&model.Instance{}, id).Error
	}
	ctx2, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := drv.Delete(ctx2, inst); err != nil {
		return err
	}
	return s.db.Delete(&model.Instance{}, id).Error
}

// PowerInstance executes start/stop/restart etc.
func (s *VirtualisService) PowerInstance(ctx context.Context, id uint, action string) (*model.Instance, error) {
	if !model.ValidAction(action) {
		// also allow hard variants
		switch action {
		case "hardStart", "hardStop", "hardRestart", "reinstall", "hard_start", "hard_stop", "hard_restart":
		default:
			return nil, BadRequest("invalid action %q", action)
		}
	}
	inst, err := s.GetInstance(id)
	if err != nil {
		return nil, err
	}
	vs := s.settings.Virtualis()
	if action == "reinstall" && !vs.AllowReinstall {
		return nil, Forbidden("reinstall disabled")
	}
	drv, ok := s.registry.Get(inst.Driver)
	if !ok {
		return nil, NotFound("driver %q not found", inst.Driver)
	}
	ctx2, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var opErr error
	switch action {
	case model.ActionStart:
		opErr = drv.Start(ctx2, inst)
		if opErr == nil {
			inst.Status = model.InstanceStatusRunning
		}
	case model.ActionStop:
		opErr = drv.Stop(ctx2, inst)
		if opErr == nil {
			inst.Status = model.InstanceStatusStopped
		}
	case model.ActionRestart:
		opErr = drv.Restart(ctx2, inst)
		if opErr == nil {
			inst.Status = model.InstanceStatusRunning
		}
	case "hardStart", "hard_start":
		opErr = drv.HardStart(ctx2, inst)
		if opErr == nil {
			inst.Status = model.InstanceStatusRunning
		}
	case "hardStop", "hard_stop":
		opErr = drv.HardStop(ctx2, inst)
		if opErr == nil {
			inst.Status = model.InstanceStatusStopped
		}
	case "hardRestart", "hard_restart":
		opErr = drv.HardRestart(ctx2, inst)
		if opErr == nil {
			inst.Status = model.InstanceStatusRunning
		}
	case "reinstall":
		var img *model.Image
		if inst.ImageID != nil {
			img, _ = s.GetImage(*inst.ImageID)
		}
		opErr = drv.Reinstall(ctx2, inst, img)
		if opErr == nil {
			inst.Status = model.InstanceStatusStopped
		}
	default:
		return nil, BadRequest("unsupported action %q", action)
	}
	if opErr != nil {
		_ = s.db.Model(inst).Update("status", model.InstanceStatusError).Error
		return nil, opErr
	}
	_ = s.db.Model(inst).Update("status", inst.Status).Error
	return inst, nil
}

// RefreshStatus queries driver for actual status and updates DB.
func (s *VirtualisService) RefreshStatus(ctx context.Context, id uint) (*model.Instance, error) {
	inst, err := s.GetInstance(id)
	if err != nil {
		return nil, err
	}
	drv, ok := s.registry.Get(inst.Driver)
	if !ok {
		return nil, NotFound("driver not found")
	}
	ctx2, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	status, err := drv.Status(ctx2, inst)
	if err != nil {
		return nil, err
	}
	if !model.ValidInstanceStatus(status) {
		status = model.InstanceStatusStopped
	}
	if status != inst.Status {
		_ = s.db.Model(inst).Update("status", status).Error
		inst.Status = status
	}
	return inst, nil
}

// Images

// ListImages returns all images.
func (s *VirtualisService) ListImages() ([]model.Image, error) {
	var items []model.Image
	if err := s.db.Order("id DESC").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// GetImage returns image by id.
func (s *VirtualisService) GetImage(id uint) (*model.Image, error) {
	var img model.Image
	if err := s.db.First(&img, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, NotFound("image not found")
		}
		return nil, err
	}
	return &img, nil
}

// CreateImageRequest is input for image creation.
type CreateImageRequest struct {
	Name     string `json:"name"`
	Driver   string `json:"driver"`
	FilePath string `json:"file_path"`
	Size     int64  `json:"size"`
	Checksum string `json:"checksum"`
}

// CreateImage validates and persists.
func (s *VirtualisService) CreateImage(req CreateImageRequest) (*model.Image, error) {
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return nil, BadRequest("image name required")
	}
	if len(req.Name) > 128 {
		return nil, BadRequest("image name too long")
	}
	req.Driver = strings.TrimSpace(strings.ToLower(req.Driver))
	if req.Driver == "" {
		req.Driver = s.settings.Virtualis().DefaultDriver
	}
	if req.Driver != "mock" && !model.ValidDriver(req.Driver) && req.Driver != "incus" {
		return nil, BadRequest("invalid driver %q", req.Driver)
	}
	req.FilePath = strings.TrimSpace(req.FilePath)
	if req.FilePath == "" {
		return nil, BadRequest("file_path required")
	}
	img := model.Image{
		Name:      req.Name,
		Driver:    req.Driver,
		FilePath:  req.FilePath,
		SizeBytes: req.Size,
		Status:    model.ImageStatusAvailable,
	}
	if err := s.db.Create(&img).Error; err != nil {
		return nil, err
	}
	return &img, nil
}

// DeleteImage removes image if not referenced.
func (s *VirtualisService) DeleteImage(id uint) error {
	var count int64
	if err := s.db.Model(&model.Instance{}).Where("image_id = ?", id).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return Conflict("image is in use by %d instances", count)
	}
	res := s.db.Delete(&model.Image{}, id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return NotFound("image not found")
	}
	return nil
}

// EnsureDefaultImages creates default images if missing.
func (s *VirtualisService) EnsureDefaultImages() error {
	defaults := []CreateImageRequest{
		{Name: "ubuntu-22.04", Driver: "qemu", FilePath: "images/ubuntu-22.04.qcow2", Size: 0},
		{Name: "debian-12", Driver: "qemu", FilePath: "images/debian-12.qcow2", Size: 0},
		{Name: "alpine-3.19", Driver: "lxc", FilePath: "images/alpine-3.19.tar.gz", Size: 0},
	}
	for _, d := range defaults {
		var cnt int64
		_ = s.db.Model(&model.Image{}).Where("name = ? AND driver = ?", d.Name, d.Driver).Count(&cnt).Error
		if cnt == 0 {
			if _, err := s.CreateImage(d); err != nil {
				return err
			}
		}
	}
	return nil
}
