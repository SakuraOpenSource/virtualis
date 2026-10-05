package service

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"regexp"
	"strings"

	"github.com/SakuraOpenSource/virtualis/internal/agentclient"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"gorm.io/gorm"
)

var vpcNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,14}$`)

type VPCInput struct {
	AgentID   uint     `json:"agent_id"`
	Name      string   `json:"name"`
	Driver    string   `json:"driver"`
	Subnet    string   `json:"subnet"`
	Gateway   string   `json:"gateway"`
	DHCPStart string   `json:"dhcp_start"`
	DHCPEnd   string   `json:"dhcp_end"`
	NAT       *bool    `json:"nat"`
	DNS       []string `json:"dns"`
	Note      string   `json:"note"`
}

type VPCView struct {
	model.VPC
	InstanceCount int64 `json:"instance_count"`
}

func normalizeVPC(req VPCInput) (*model.VPC, error) {
	name := strings.TrimSpace(req.Name)
	if !vpcNamePattern.MatchString(name) {
		return nil, BadRequest("网络名称需为 2-15 位小写字母、数字或连字符")
	}
	ip, subnet, err := net.ParseCIDR(strings.TrimSpace(req.Subnet))
	if err != nil || ip.To4() == nil {
		return nil, BadRequest("子网必须为 IPv4 CIDR")
	}
	ones, bits := subnet.Mask.Size()
	if bits != 32 || ones < 16 || ones > 29 {
		return nil, BadRequest("VPC 子网前缀需在 /16-/29 之间")
	}
	base := binary.BigEndian.Uint32(subnet.IP.To4())
	broadcast := base | ^binary.BigEndian.Uint32(subnet.Mask)
	usable := func(raw string) (uint32, bool) {
		p := net.ParseIP(strings.TrimSpace(raw))
		if p == nil || p.To4() == nil || !subnet.Contains(p) {
			return 0, false
		}
		v := binary.BigEndian.Uint32(p.To4())
		return v, v > base && v < broadcast
	}
	gw, ok := usable(req.Gateway)
	if !ok {
		return nil, BadRequest("网关必须是子网内的可用 IPv4 地址")
	}
	start, end := strings.TrimSpace(req.DHCPStart), strings.TrimSpace(req.DHCPEnd)
	if (start == "") != (end == "") {
		return nil, BadRequest("DHCP 起止地址必须同时填写")
	}
	if start == "" {
		a, b := base+10, base+250
		if b >= broadcast {
			b = broadcast - 1
		}
		if a >= b {
			a = base + 2
		}
		if gw >= a && gw <= b {
			if gw-a > b-gw {
				b = gw - 1
			} else {
				a = gw + 1
			}
		}
		start, end = vpcIPv4(a), vpcIPv4(b)
	}
	a, validA := usable(start)
	b, validB := usable(end)
	if !validA || !validB || a > b || (gw >= a && gw <= b) {
		return nil, BadRequest("DHCP 范围必须位于子网内且不能包含网关")
	}
	network, err := model.NormalizeNetworkConfig(model.NetworkConfig{Mode: model.NetworkModeVPC, DNS: req.DNS})
	if err != nil {
		return nil, BadRequest("%s", err)
	}
	driver := strings.ToLower(strings.TrimSpace(req.Driver))
	if driver == "" {
		driver = model.DriverIncus
	}
	if driver != model.DriverIncus && driver != model.DriverQEMU {
		return nil, BadRequest("VPC 驱动必须为 incus 或 qemu")
	}
	if len(req.Note) > 255 {
		return nil, BadRequest("备注过长")
	}
	nat := req.NAT == nil || *req.NAT
	return &model.VPC{AgentID: req.AgentID, Name: name, Driver: driver, Subnet: subnet.String(), Gateway: strings.TrimSpace(req.Gateway), DHCPStart: start, DHCPEnd: end, NAT: nat, DNS: network.DNS, Note: strings.TrimSpace(req.Note)}, nil
}

func vpcIPv4(v uint32) string {
	ip := make(net.IP, 4)
	binary.BigEndian.PutUint32(ip, v)
	return ip.String()
}

func (s *VirtualisService) GetVPC(id uint) (*model.VPC, error) {
	var vpc model.VPC
	if err := s.db.First(&vpc, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, NotFound("VPC 不存在")
		}
		return nil, err
	}
	return &vpc, nil
}

func (s *VirtualisService) ListVPCs(agentID uint) ([]VPCView, error) {
	query := s.db.Model(&model.VPC{})
	if agentID != 0 {
		query = query.Where("agent_id = ?", agentID)
	}
	var items []model.VPC
	if err := query.Order("id DESC").Find(&items).Error; err != nil {
		return nil, err
	}
	views := make([]VPCView, 0, len(items))
	for _, item := range items {
		view := VPCView{VPC: item}
		if err := s.db.Model(&model.Instance{}).Where("vpc_id = ?", item.ID).Count(&view.InstanceCount).Error; err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, nil
}

func (s *VirtualisService) CreateVPC(ctx context.Context, req VPCInput) (*model.VPC, error) {
	vpc, err := normalizeVPC(req)
	if err != nil {
		return nil, err
	}
	agent, err := NewAgentService(s.db).Get(vpc.AgentID)
	if err != nil {
		return nil, err
	}
	if !agent.IsOnline() {
		return nil, Conflict("被控节点当前不在线")
	}
	var count int64
	if err := s.db.Model(&model.VPC{}).Where("agent_id = ? AND name = ?", vpc.AgentID, vpc.Name).Count(&count).Error; err != nil {
		return nil, err
	}
	if count > 0 {
		return nil, Conflict("该节点已有同名 VPC")
	}
	client, err := s.agentClient(agent)
	if err != nil {
		return nil, err
	}
	if err := client.CreateNetwork(ctx, agentclient.NetworkSpec{Name: vpc.Name, Driver: vpc.Driver, Subnet: vpc.Subnet, Gateway: vpc.Gateway, DHCPStart: vpc.DHCPStart, DHCPEnd: vpc.DHCPEnd, NAT: vpc.NAT, DNS: vpc.DNS}); err != nil {
		return nil, agentFailure(err)
	}
	if err := s.db.Create(vpc).Error; err != nil {
		_ = client.DeleteNetwork(ctx, vpc.Name, vpc.Driver)
		return nil, err
	}
	return vpc, nil
}

func (s *VirtualisService) DeleteVPC(ctx context.Context, id uint) error {
	vpc, err := s.GetVPC(id)
	if err != nil {
		return err
	}
	var count int64
	if err := s.db.Model(&model.Instance{}).Where("vpc_id = ?", id).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return Conflict("VPC 仍有实例引用，请先迁出或彻底删除")
	}
	agent, err := NewAgentService(s.db).Get(vpc.AgentID)
	if err != nil {
		return err
	}
	client, err := s.agentClient(agent)
	if err != nil {
		return err
	}
	if err := client.DeleteNetwork(ctx, vpc.Name, vpc.Driver); err != nil {
		return agentFailure(err)
	}
	return s.db.Delete(vpc).Error
}
