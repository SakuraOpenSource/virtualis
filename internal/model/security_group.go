package model

// SecurityGroup 是提供商管理的可复用防火墙配置，不包含执行命令或凭证。
type SecurityGroup struct {
	Base
	Name          string              `gorm:"size:64;not null" json:"name"`
	Revision      uint64              `gorm:"not null;default:0" json:"-"`
	Description   string              `gorm:"size:255" json:"description"`
	IngressPolicy string              `gorm:"size:8;not null;default:drop" json:"ingress_policy"`
	EgressPolicy  string              `gorm:"size:8;not null;default:accept" json:"egress_policy"`
	Rules         []SecurityGroupRule `gorm:"foreignKey:SecurityGroupID" json:"rules"`
}

// SecurityGroupRule 复用实例规则字段，但不隶属任何实例。
type SecurityGroupRule struct {
	Base
	SecurityGroupID uint   `gorm:"index;not null" json:"security_group_id"`
	Direction       string `gorm:"size:8;not null" json:"direction"`
	Action          string `gorm:"size:8;not null" json:"action"`
	Protocol        string `gorm:"size:8;not null" json:"protocol"`
	PortStart       int    `json:"port_start"`
	PortEnd         int    `json:"port_end"`
	CIDR            string `gorm:"size:64" json:"cidr"`
	Priority        int    `json:"priority"`
	Enabled         bool   `gorm:"not null" json:"enabled"`
	Remark          string `gorm:"size:255" json:"remark"`
}

// InstanceSecurityGroup 保留回收站绑定，仅经永久删除解除。
type InstanceSecurityGroup struct {
	InstanceID      uint `gorm:"primaryKey" json:"instance_id"`
	SecurityGroupID uint `gorm:"primaryKey;index" json:"security_group_id"`
}

// FirewallPolicy 为 nil 时保留历史逐实例规则语义。
type FirewallPolicy struct {
	Ingress string `json:"ingress"`
	Egress  string `json:"egress"`
}
