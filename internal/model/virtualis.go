package model

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
)

const (
	DriverIncus = "incus"
	DriverQEMU  = "qemu"
	DriverAuto  = "auto"
)

func AllDrivers() []string {
	return []string{DriverAuto, DriverIncus, DriverQEMU}
}
func ValidDriver(d string) bool {
	switch d {
	case DriverIncus, DriverQEMU, DriverAuto:
		return true
	}
	return false
}

const (
	// NetworkModeNAT 是 NAT 模式：实例经主机转发共享出口 IP。
	NetworkModeNAT = "nat"
	// NetworkModeDedicated 是独立 IP 模式：实例网卡直连主机网段。
	// 仅当主机拥有至少 2 个 IPv4 地址时可用（创建时校验）。
	NetworkModeDedicated = "dedicated"
	// NetworkModeNone 关闭实例网络。
	NetworkModeNone = "none"
)

const (
	InstanceTypeContainer = "container"
	InstanceTypeVM        = "vm"
)

const (
	InstanceStatusPending   = "pending"
	InstanceStatusCreating  = "creating"
	InstanceStatusRunning   = "running"
	InstanceStatusStopped   = "stopped"
	InstanceStatusSuspended = "suspended"
	InstanceStatusError     = "error"
	InstanceStatusDeleting  = "deleting"
)

const (
	ImageStatusAvailable   = "available"
	ImageStatusDownloading = "downloading"
	ImageStatusError       = "error"
)

const (
	ImageTypeDisk = "disk"
	ImageTypeISO  = "iso"
)

type InstanceSpec struct {
	CPU      int    `json:"cpu"`
	MemoryMB int    `json:"memory_mb"`
	DiskGB   int    `json:"disk_gb"`
	Arch     string `json:"arch,omitempty"`
}

type NetworkConfig struct {
	Mode          string   `json:"mode"`
	Bridge        string   `json:"bridge,omitempty"`
	MAC           string   `json:"mac,omitempty"`
	IPv4          string   `json:"ipv4,omitempty"`
	Gateway       string   `json:"gateway,omitempty"`
	DNS           []string `json:"dns,omitempty"`
	BandwidthMbps int      `json:"bandwidth_mbps,omitempty"`
}

func NormalizeNetworkConfig(network NetworkConfig) (NetworkConfig, error) {
	network.Mode = strings.ToLower(strings.TrimSpace(network.Mode))
	// 历史数据里的 bridge 即独立 IP 模式，统一归一化。
	if network.Mode == "bridge" {
		network.Mode = NetworkModeDedicated
	}
	if network.Mode == "" {
		network.Mode = NetworkModeNAT
	}
	if network.Mode != NetworkModeNAT && network.Mode != NetworkModeDedicated && network.Mode != NetworkModeNone {
		return network, errors.New("网络模式必须是 nat、dedicated 或 none")
	}
	// Dedicated 下 Bridge 存放挂载目标：主机网桥或物理网卡名。
	// 留空表示由被控自动选择第一个有地址的物理网卡。
	network.Bridge = strings.TrimSpace(network.Bridge)
	if len(network.Bridge) > 64 || strings.IndexFunc(network.Bridge, func(r rune) bool {
		return !(r == '-' || r == '_' || r == '.' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'))
	}) >= 0 {
		return network, errors.New("网卡名称格式无效")
	}
	if network.IPv4 == "" && network.Gateway == "" && network.Mode == NetworkModeDedicated {
		// 独立 IP 至少要声明地址或网关之一，避免创建后完全不可达。
		// 两者都空时仍允许：由局域网 DHCP 分配，字段仅作展示。
		_ = network
	}
	if network.MAC != "" {
		mac, err := net.ParseMAC(network.MAC)
		if err != nil || len(mac) != 6 {
			return network, errors.New("MAC 地址格式无效")
		}
		network.MAC = strings.ToLower(mac.String())
	}
	if network.IPv4 != "" {
		if ip, _, err := net.ParseCIDR(network.IPv4); err == nil {
			if ip.To4() == nil {
				return network, errors.New("IPv4 地址格式无效")
			}
		} else if ip := net.ParseIP(network.IPv4); ip == nil || ip.To4() == nil {
			return network, errors.New("IPv4 地址格式无效")
		}
	}
	if network.Gateway != "" {
		ip := net.ParseIP(network.Gateway)
		if ip == nil || ip.To4() == nil {
			return network, errors.New("网关地址格式无效")
		}
	}
	if len(network.DNS) > 4 {
		return network, errors.New("DNS 最多填写 4 个地址")
	}
	for i, dns := range network.DNS {
		dns = strings.TrimSpace(dns)
		ip := net.ParseIP(dns)
		if ip == nil {
			return network, errors.New("DNS 地址格式无效")
		}
		network.DNS[i] = ip.String()
	}
	if network.BandwidthMbps < 0 || network.BandwidthMbps > 100000 {
		return network, errors.New("带宽需在 0-100000 Mbps 之间")
	}
	return network, nil
}

type Instance struct {
	Base
	Name        string        `gorm:"uniqueIndex;size:64;not null" json:"name"`
	DisplayName string        `gorm:"size:128" json:"display_name"`
	Driver      string        `gorm:"size:16;not null;default:auto" json:"driver"`
	Type        string        `gorm:"size:16;not null;default:container" json:"type"`
	Status      string        `gorm:"size:16;not null;default:pending" json:"status"`
	ImageID     *uint         `gorm:"index" json:"image_id"`
	ImageName   string        `gorm:"size:128" json:"image_name"`
	Spec        InstanceSpec  `gorm:"type:text;serializer:json" json:"spec"`
	Network     NetworkConfig `gorm:"type:text;serializer:json" json:"network"`
	IP          string        `gorm:"size:64" json:"ip"`
	ConfigJSON  string        `gorm:"type:text" json:"-"`
	// MaxNATMappings 是该实例允许创建的 NAT 映射上限，0 表示不限。
	MaxNATMappings int    `gorm:"not null;default:0" json:"max_nat_mappings"`
	OwnerID        *uint  `gorm:"index" json:"owner_id"`
	Owner          *User  `gorm:"foreignKey:OwnerID" json:"owner,omitempty"`
	Image          *Image `gorm:"foreignKey:ImageID" json:"image,omitempty"`
	AgentID        *uint  `gorm:"index" json:"agent_id"`
	Agent          *Agent `gorm:"foreignKey:AgentID" json:"agent,omitempty"`
	// NATMappings 是该实例的 NAT 端口转发清单，由被控在开机时应用。
	NATMappings []NATMapping `gorm:"foreignKey:InstanceID" json:"nat_mappings,omitempty"`
	// SSHPassword 是运行时注入的 root 密码（存 ConfigJSON），只在详情
	// 接口填充，不落列。
	SSHPassword string `gorm:"-" json:"ssh_password,omitempty"`
}

// NATMapping 是实例的一条 NAT 端口转发：被控主机 HostPort → 实例 GuestPort。
type NATMapping struct {
	Base
	InstanceID uint   `gorm:"index;not null" json:"instance_id"`
	AgentID    uint   `gorm:"index" json:"agent_id"`
	Protocol   string `gorm:"size:8;not null;default:tcp" json:"protocol"`
	HostPort   int    `gorm:"not null" json:"host_port"`
	GuestPort  int    `gorm:"not null" json:"guest_port"`
	Remark     string `gorm:"size:64" json:"remark"`
	// Auto 标记自动创建的映射（如 SSH 端口），前端据此展示。
	Auto bool `gorm:"not null;default:false" json:"auto"`
}

// NAT 宿主端口的自动分配范围。
const (
	NATPortMin = 20000
	NATPortMax = 29999
)

// ValidNATProtocol 报告 p 是否为受支持的转发协议。
func ValidNATProtocol(p string) bool {
	return p == "tcp" || p == "udp"
}

// instanceConfig 是 ConfigJSON 的结构化形态。密码明文落库是管理页
// 「可查看并连接」的明确需求，防护口径与 API Key 明文不落库不同。
type instanceConfig struct {
	SSHPassword string `json:"ssh_password,omitempty"`
}

// LoadSSHPassword 从 ConfigJSON 解析 root 密码，未设置时返回空。
func (i *Instance) LoadSSHPassword() string {
	var cfg instanceConfig
	if i.ConfigJSON != "" && json.Unmarshal([]byte(i.ConfigJSON), &cfg) == nil {
		return cfg.SSHPassword
	}
	return ""
}

// StoreSSHPassword 把 root 密码写进 ConfigJSON。
func (i *Instance) StoreSSHPassword(password string) {
	cfg := instanceConfig{SSHPassword: password}
	if raw, err := json.Marshal(cfg); err == nil {
		i.ConfigJSON = string(raw)
	}
}

type Image struct {
	Base
	Name         string `gorm:"uniqueIndex;size:128;not null" json:"name"`
	DisplayName  string `gorm:"size:128" json:"display_name"`
	Description  string `gorm:"type:text" json:"description"`
	Driver       string `gorm:"size:16;not null;default:auto" json:"driver"`
	Type         string `gorm:"size:16;not null;default:disk" json:"type"`
	OSType       string `gorm:"size:64" json:"os_type"`
	OSVersion    string `gorm:"size:64" json:"os_version"`
	Arch         string `gorm:"size:16" json:"arch"`
	OriginalName string `gorm:"size:255" json:"original_name"`
	MimeType     string `gorm:"size:128" json:"mime_type"`
	FilePath     string `gorm:"size:255" json:"file_path"`
	// ExtraPath 是 Incus 分割镜像的第二文件（meta.tar.xz）的存储路径。
	ExtraPath string `gorm:"size:255" json:"extra_path"`
	SizeBytes int64  `gorm:"not null;default:0" json:"size_bytes"`
	Checksum  string `gorm:"size:128" json:"checksum"`
	Status    string `gorm:"size:16;not null;default:available" json:"status"`
	IsPublic  bool   `gorm:"not null;default:true" json:"is_public"`
}

const (
	ActionStart       = "start"
	ActionStop        = "stop"
	ActionRestart     = "restart"
	ActionHardStart   = "hard_start"
	ActionHardStop    = "hard_stop"
	ActionHardRestart = "hard_restart"
	ActionReinstall   = "reinstall"
)

func AllInstanceActions() []string {
	return []string{ActionStart, ActionStop, ActionRestart, ActionHardStart, ActionHardStop, ActionHardRestart, ActionReinstall}
}

func ValidAction(a string) bool {
	for _, v := range AllInstanceActions() {
		if v == a {
			return true
		}
	}
	// allow underscore variants for compatibility
	switch a {
	case "hard_start", "hard_stop", "hard_restart":
		return true
	}
	return false
}

type StringList []string

func (s StringList) Value() (driver.Value, error) {
	if len(s) == 0 {
		return "[]", nil
	}
	raw, err := json.Marshal([]string(s))
	if err != nil {
		return nil, err
	}
	return string(raw), nil
}
func (s *StringList) Scan(src any) error {
	if src == nil {
		*s = nil
		return nil
	}
	var raw []byte
	switch v := src.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return fmt.Errorf("model: 无法把 %T 解析为 StringList", src)
	}
	if len(raw) == 0 {
		*s = nil
		return nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return errors.New("model: StringList 不是合法的 JSON 数组")
	}
	*s = out
	return nil
}

func InstanceStatusLabel(s string) string {
	switch s {
	case InstanceStatusRunning:
		return "运行中"
	case InstanceStatusStopped:
		return "已关机"
	case InstanceStatusPending:
		return "待创建"
	case InstanceStatusCreating:
		return "创建中"
	case InstanceStatusSuspended:
		return "已暂停"
	case InstanceStatusError:
		return "异常"
	case InstanceStatusDeleting:
		return "删除中"
	default:
		return s
	}
}

func ValidInstanceStatus(s string) bool {
	switch s {
	case InstanceStatusPending, InstanceStatusCreating, InstanceStatusRunning, InstanceStatusStopped, InstanceStatusSuspended, InstanceStatusError, InstanceStatusDeleting:
		return true
	}
	return false
}

func NormalizeInstanceSpec(spec InstanceSpec) (InstanceSpec, error) {
	if spec.CPU <= 0 {
		spec.CPU = 1
	}
	if spec.CPU > 64 {
		return spec, errors.New("CPU 核数需在 1-64 之间")
	}
	if spec.MemoryMB <= 0 {
		spec.MemoryMB = 1024
	}
	if spec.MemoryMB > 262144 {
		return spec, errors.New("内存需在 1-262144 MB 之间")
	}
	if spec.DiskGB <= 0 {
		spec.DiskGB = 20
	}
	if spec.DiskGB > 4096 {
		return spec, errors.New("磁盘需在 1-4096 GB 之间")
	}
	if spec.Arch == "" {
		spec.Arch = "x86_64"
	}
	return spec, nil
}

func InstancePowerOps(status string) []string {
	switch status {
	case InstanceStatusRunning:
		return []string{ActionStop, ActionRestart, ActionHardStop, ActionHardRestart, ActionReinstall}
	case InstanceStatusStopped:
		return []string{ActionStart, ActionHardStart, ActionReinstall}
	case InstanceStatusError:
		return []string{ActionStart, ActionHardStart, ActionReinstall}
	default:
		return []string{}
	}
}
