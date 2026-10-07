package service

import (
	"context"
	"errors"
	"github.com/SakuraOpenSource/virtualis/internal/agentclient"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"gorm.io/gorm"
	"sort"
)

// effectiveFirewall 保持本地规则私有，按优先级、本地 ID、组 ID/规则 ID 稳定合并。
func effectiveFirewall(inst *model.Instance) ([]agentclient.FirewallRule, *model.FirewallPolicy) {
	rules := []agentclient.FirewallRule{}
	local := append([]model.FirewallRule(nil), inst.FirewallRules...)
	sort.SliceStable(local, func(i, j int) bool { return local[i].ID < local[j].ID })
	for _, r := range toWireFirewall(local) {
		if r.Enabled {
			rules = append(rules, r)
		}
	}
	groups := append([]model.SecurityGroup(nil), inst.SecurityGroups...)
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].ID < groups[j].ID })
	var policy *model.FirewallPolicy
	if len(groups) > 0 {
		policy = &model.FirewallPolicy{Ingress: "accept", Egress: "accept"}
	}
	for _, g := range groups {
		if g.IngressPolicy != "accept" {
			policy.Ingress = "drop"
		}
		if g.EgressPolicy != "accept" {
			policy.Egress = "drop"
		}
		groupRules := append([]model.SecurityGroupRule(nil), g.Rules...)
		sort.SliceStable(groupRules, func(i, j int) bool { return groupRules[i].ID < groupRules[j].ID })
		for _, r := range groupRules {
			if r.Enabled {
				rules = append(rules, agentclient.FirewallRule{ID: r.ID, Direction: r.Direction, Action: r.Action, Protocol: r.Protocol, PortStart: r.PortStart, PortEnd: r.PortEnd, CIDR: r.CIDR, Priority: r.Priority, Enabled: true, Remark: r.Remark})
			}
		}
	}
	sort.SliceStable(rules, func(i, j int) bool { return rules[i].Priority < rules[j].Priority })
	return rules, policy
}

// SecurityGroupBindingView 为 GET/PUT 共用，未绑定时展示 accept/accept，wire 仍省略策略。
type SecurityGroupBindingView struct {
	SecurityGroupIDs []uint                     `json:"security_group_ids"`
	Groups           []model.SecurityGroup      `json:"groups"`
	EffectiveRules   []agentclient.FirewallRule `json:"effective_rules"`
	FirewallPolicy   *model.FirewallPolicy      `json:"firewall_policy"`
}

func (s *VirtualisService) InstanceSecurityGroups(id uint) (*SecurityGroupBindingView, error) {
	inst, err := s.GetInstance(id)
	if err != nil {
		return nil, err
	}
	rules, policy := effectiveFirewall(inst)
	if policy == nil {
		policy = &model.FirewallPolicy{Ingress: "accept", Egress: "accept"}
	}
	groups := inst.SecurityGroups
	if groups == nil {
		groups = []model.SecurityGroup{}
	}
	ids := []uint{}
	for i := range groups {
		ids = append(ids, groups[i].ID)
		if groups[i].Rules == nil {
			groups[i].Rules = []model.SecurityGroupRule{}
		}
	}
	return &SecurityGroupBindingView{SecurityGroupIDs: ids, Groups: groups, EffectiveRules: rules, FirewallPolicy: policy}, nil
}

func validateSecurityGroupIDs(ids []uint) error {
	if len(ids) > 16 {
		return BadRequest("每个实例最多绑定 16 个安全组")
	}
	seen := map[uint]bool{}
	for _, id := range ids {
		if id == 0 || seen[id] {
			return BadRequest("安全组 ID 必须是非零且不重复")
		}
		seen[id] = true
	}
	return nil
}

// bindSecurityGroups 必须在实例事务中调用；行写锁与组删除共享以避免悬空引用。
func bindSecurityGroups(tx *gorm.DB, id uint, ids []uint) error {
	if err := validateSecurityGroupIDs(ids); err != nil {
		return err
	}
	sorted := append([]uint(nil), ids...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	for _, groupID := range sorted {
		res := tx.Model(&model.SecurityGroup{}).Where("id = ?", groupID).UpdateColumn("revision", gorm.Expr("revision + 1"))
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return NotFound("安全组不存在")
		}
	}
	if err := tx.Where("instance_id = ?", id).Delete(&model.InstanceSecurityGroup{}).Error; err != nil {
		return err
	}
	for _, groupID := range sorted {
		if err := tx.Create(&model.InstanceSecurityGroup{InstanceID: id, SecurityGroupID: groupID}).Error; err != nil {
			return err
		}
	}
	return markFirewallPending(tx, id)
}

func (s *VirtualisService) SetInstanceSecurityGroups(ctx context.Context, id uint, ids []uint) (result *SecurityGroupBindingView, err error) {
	if err = validateSecurityGroupIDs(ids); err != nil {
		return nil, err
	}
	guard, err := s.beginOperation(ctx, id, "security_groups")
	if err != nil {
		return nil, err
	}
	defer guard.finish(&err)
	inst, err := s.GetInstance(id)
	if err != nil {
		return nil, err
	}
	if len(ids) > 0 {
		if inst.Agent == nil {
			return nil, Conflict("实例没有关联被控节点")
		}
		client, e := s.agentClient(inst.Agent)
		if e != nil {
			return nil, e
		}
		if e = client.RequireFirewallPolicy(ctx, agentclient.Instance{Driver: inst.Driver, FirewallPolicy: &model.FirewallPolicy{Ingress: "drop", Egress: "accept"}}); e != nil {
			return nil, Conflict("无法绑定安全组：%s", e)
		}
	}
	if err = s.db.Transaction(func(tx *gorm.DB) error { return bindSecurityGroups(tx, id, ids) }); err != nil {
		return nil, err
	}
	if err = s.syncFirewallIfRunning(ctx, id); err != nil {
		return nil, err
	}
	return s.InstanceSecurityGroups(id)
}

func securityGroupNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return NotFound("安全组不存在")
	}
	return err
}
