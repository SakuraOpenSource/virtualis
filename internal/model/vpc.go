package model

type VPC struct {
	Base
	AgentID   uint       `gorm:"uniqueIndex:idx_vpc_agent_name,priority:1;not null" json:"agent_id"`
	Name      string     `gorm:"uniqueIndex:idx_vpc_agent_name,priority:2;size:32;not null" json:"name"`
	Driver    string     `gorm:"size:16;not null" json:"driver"`
	Subnet    string     `gorm:"size:64;not null" json:"subnet"`
	Gateway   string     `gorm:"size:64;not null" json:"gateway"`
	DHCPStart string     `gorm:"size:64" json:"dhcp_start"`
	DHCPEnd   string     `gorm:"size:64" json:"dhcp_end"`
	NAT       bool       `gorm:"not null" json:"nat"`
	DNS       StringList `gorm:"type:text" json:"dns"`
	Note      string     `gorm:"size:255" json:"note"`
}

type FirewallRule struct {
	Base
	InstanceID uint   `gorm:"index;not null" json:"instance_id"`
	AgentID    uint   `gorm:"index;not null" json:"agent_id"`
	Direction  string `gorm:"size:8;not null" json:"direction"`
	Action     string `gorm:"size:8;not null" json:"action"`
	Protocol   string `gorm:"size:8;not null" json:"protocol"`
	PortStart  int    `json:"port_start"`
	PortEnd    int    `json:"port_end"`
	CIDR       string `gorm:"size:64" json:"cidr"`
	Priority   int    `json:"priority"`
	Enabled    bool   `gorm:"not null" json:"enabled"`
	Remark     string `gorm:"size:255" json:"remark"`
}
