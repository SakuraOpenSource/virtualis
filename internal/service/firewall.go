package service

import (
	"context"
	"errors"
	"net"
	"strings"

	"github.com/SakuraOpenSource/virtualis/internal/agentclient"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"gorm.io/gorm"
)

type FirewallInput struct {
	Direction string `json:"direction"`
	Action    string `json:"action"`
	Protocol  string `json:"protocol"`
	PortStart int    `json:"port_start"`
	PortEnd   int    `json:"port_end"`
	CIDR      string `json:"cidr"`
	Priority  int    `json:"priority"`
	Enabled   *bool  `json:"enabled"`
	Remark    string `json:"remark"`
}

func normalizeFirewall(req FirewallInput) (model.FirewallRule, error) {
	rule := model.FirewallRule{Direction: strings.ToLower(strings.TrimSpace(req.Direction)), Action: strings.ToLower(strings.TrimSpace(req.Action)), Protocol: strings.ToLower(strings.TrimSpace(req.Protocol)), PortStart: req.PortStart, PortEnd: req.PortEnd, CIDR: strings.TrimSpace(req.CIDR), Priority: req.Priority, Enabled: req.Enabled == nil || *req.Enabled, Remark: strings.TrimSpace(req.Remark)}
	if rule.Direction != "in" && rule.Direction != "out" {
		return rule, BadRequest("方向必须是 in 或 out")
	}
	if rule.Action != "accept" && rule.Action != "drop" {
		return rule, BadRequest("动作必须是 accept 或 drop")
	}
	if rule.Protocol == "" {
		rule.Protocol = "any"
	}
	if rule.Protocol != "tcp" && rule.Protocol != "udp" && rule.Protocol != "icmp" && rule.Protocol != "any" {
		return rule, BadRequest("不支持的防火墙协议")
	}
	if rule.PortStart < 0 || rule.PortStart > 65535 || rule.PortEnd < 0 || rule.PortEnd > 65535 {
		return rule, BadRequest("端口需在 1-65535 之间，0 表示不限")
	}
	if rule.PortStart == 0 && rule.PortEnd != 0 {
		return rule, BadRequest("端口范围必须填写起始端口")
	}
	if rule.PortStart > 0 {
		if rule.Protocol != "tcp" && rule.Protocol != "udp" {
			return rule, BadRequest("只有 TCP/UDP 可以指定端口")
		}
		if rule.PortEnd == 0 {
			rule.PortEnd = rule.PortStart
		}
		if rule.PortEnd < rule.PortStart {
			return rule, BadRequest("结束端口不能小于起始端口")
		}
	}
	if rule.CIDR != "" {
		if ip := net.ParseIP(rule.CIDR); ip != nil && ip.To4() != nil {
			rule.CIDR = ip.String() + "/32"
		}
		ip, block, err := net.ParseCIDR(rule.CIDR)
		if err != nil || ip.To4() == nil {
			return rule, BadRequest("来源/目标必须为 IPv4 地址或 CIDR")
		}
		rule.CIDR = block.String()
	}
	if rule.Priority < 0 || rule.Priority > 65535 || len(rule.Remark) > 255 {
		return rule, BadRequest("优先级或备注长度无效")
	}
	return rule, nil
}

func toWireFirewall(rules []model.FirewallRule) []agentclient.FirewallRule {
	items := make([]agentclient.FirewallRule, 0, len(rules))
	for _, r := range rules {
		items = append(items, agentclient.FirewallRule{ID: r.ID, Direction: r.Direction, Action: r.Action, Protocol: r.Protocol, PortStart: r.PortStart, PortEnd: r.PortEnd, CIDR: r.CIDR, Priority: r.Priority, Enabled: r.Enabled, Remark: r.Remark})
	}
	return items
}

func (s *VirtualisService) ListFirewall(instanceID uint) ([]model.FirewallRule, error) {
	inst, err := s.GetInstance(instanceID)
	if err != nil {
		return nil, err
	}
	return inst.FirewallRules, nil
}

func (s *VirtualisService) CreateFirewall(ctx context.Context, instanceID uint, req FirewallInput) (*model.FirewallRule, error) {
	inst, err := s.GetInstance(instanceID)
	if err != nil {
		return nil, err
	}
	if inst.AgentID == nil {
		return nil, Conflict("实例没有关联节点")
	}
	rule, err := normalizeFirewall(req)
	if err != nil {
		return nil, err
	}
	rule.InstanceID, rule.AgentID = instanceID, *inst.AgentID
	if err := s.db.Create(&rule).Error; err != nil {
		return nil, err
	}
	return &rule, s.syncFirewallIfRunning(ctx, instanceID)
}

func (s *VirtualisService) UpdateFirewall(ctx context.Context, id uint, req FirewallInput) (*model.FirewallRule, error) {
	var old model.FirewallRule
	if err := s.db.First(&old, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, NotFound("规则不存在")
		}
		return nil, err
	}
	if _, err := s.GetInstance(old.InstanceID); err != nil {
		return nil, err
	}
	rule, err := normalizeFirewall(req)
	if err != nil {
		return nil, err
	}
	rule.Base, rule.InstanceID, rule.AgentID = old.Base, old.InstanceID, old.AgentID
	if err := s.db.Save(&rule).Error; err != nil {
		return nil, err
	}
	return &rule, s.syncFirewallIfRunning(ctx, rule.InstanceID)
}

func (s *VirtualisService) DeleteFirewall(ctx context.Context, id uint) error {
	var rule model.FirewallRule
	if err := s.db.First(&rule, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return NotFound("规则不存在")
		}
		return err
	}
	if _, err := s.GetInstance(rule.InstanceID); err != nil {
		return err
	}
	if err := s.db.Delete(&rule).Error; err != nil {
		return err
	}
	return s.syncFirewallIfRunning(ctx, rule.InstanceID)
}

func (s *VirtualisService) syncFirewallIfRunning(ctx context.Context, id uint) error {
	inst, err := s.GetInstance(id)
	if err != nil {
		return err
	}
	if inst.Status != model.InstanceStatusRunning || inst.Agent == nil {
		return nil
	}
	client, err := s.agentClient(inst.Agent)
	if err != nil {
		return err
	}
	if err := client.ApplyFirewall(ctx, toWireInstance(inst, inst.Image)); err != nil {
		return Conflict("规则已保存，将在下次对账重试；当前下发失败：%s", err)
	}
	return nil
}
