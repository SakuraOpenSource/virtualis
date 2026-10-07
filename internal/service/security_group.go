package service

import (
	"context"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"gorm.io/gorm"
	"strings"
)

// SecurityGroupInput 创建时省略策略采用安全默认值。
type SecurityGroupInput struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	IngressPolicy string `json:"ingress_policy"`
	EgressPolicy  string `json:"egress_policy"`
}

func normalizeSecurityGroup(in SecurityGroupInput) (model.SecurityGroup, error) {
	g := model.SecurityGroup{Name: strings.TrimSpace(in.Name), Description: strings.TrimSpace(in.Description), IngressPolicy: in.IngressPolicy, EgressPolicy: in.EgressPolicy, Rules: []model.SecurityGroupRule{}}
	if len(g.Name) == 0 || len(g.Name) > 64 || len(g.Description) > 255 {
		return g, BadRequest("安全组名称需为 1-64 字节，描述最多 255 字节")
	}
	if g.IngressPolicy == "" {
		g.IngressPolicy = "drop"
	}
	if g.EgressPolicy == "" {
		g.EgressPolicy = "accept"
	}
	if (g.IngressPolicy != "accept" && g.IngressPolicy != "drop") || (g.EgressPolicy != "accept" && g.EgressPolicy != "drop") {
		return g, BadRequest("安全组策略只支持 accept/drop")
	}
	return g, nil
}

func (s *VirtualisService) CreateSecurityGroup(in SecurityGroupInput) (*model.SecurityGroup, error) {
	g, err := normalizeSecurityGroup(in)
	if err != nil {
		return nil, err
	}
	if err = s.db.Create(&g).Error; err != nil {
		return nil, err
	}
	return &g, nil
}

func (s *VirtualisService) ListSecurityGroups() ([]model.SecurityGroup, error) {
	groups := []model.SecurityGroup{}
	err := s.db.Preload("Rules").Order("id ASC").Find(&groups).Error
	for i := range groups {
		if groups[i].Rules == nil {
			groups[i].Rules = []model.SecurityGroupRule{}
		}
	}
	return groups, err
}

func (s *VirtualisService) GetSecurityGroup(id uint) (*model.SecurityGroup, error) {
	var g model.SecurityGroup
	if err := s.db.Preload("Rules", func(db *gorm.DB) *gorm.DB { return db.Order("priority ASC, id ASC") }).First(&g, id).Error; err != nil {
		return nil, securityGroupNotFound(err)
	}
	if g.Rules == nil {
		g.Rules = []model.SecurityGroupRule{}
	}
	return &g, nil
}

// SecurityGroupPatch 使用指针区分省略与清空，不把 PATCH 当成创建。
type SecurityGroupPatch struct {
	Name          *string `json:"name"`
	Description   *string `json:"description"`
	IngressPolicy *string `json:"ingress_policy"`
	EgressPolicy  *string `json:"egress_policy"`
}

func lockSecurityGroup(tx *gorm.DB, id uint) error {
	res := tx.Model(&model.SecurityGroup{}).Where("id = ?", id).UpdateColumn("revision", gorm.Expr("revision + 1"))
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return NotFound("安全组不存在")
	}
	return nil
}

func (s *VirtualisService) UpdateSecurityGroup(ctx context.Context, id uint, in SecurityGroupPatch) (*model.SecurityGroup, error) {
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := lockSecurityGroup(tx, id); err != nil {
			return err
		}
		g, err := NewVirtualisService(tx).GetSecurityGroup(id)
		if err != nil {
			return err
		}
		if in.Name != nil {
			g.Name = *in.Name
		}
		if in.Description != nil {
			g.Description = *in.Description
		}
		if in.IngressPolicy != nil {
			if *in.IngressPolicy == "" {
				return BadRequest("策略不能为空")
			}
			g.IngressPolicy = *in.IngressPolicy
		}
		if in.EgressPolicy != nil {
			if *in.EgressPolicy == "" {
				return BadRequest("策略不能为空")
			}
			g.EgressPolicy = *in.EgressPolicy
		}
		normalized, err := normalizeSecurityGroup(SecurityGroupInput{Name: g.Name, Description: g.Description, IngressPolicy: g.IngressPolicy, EgressPolicy: g.EgressPolicy})
		if err != nil {
			return err
		}
		if err := tx.Model(g).Updates(map[string]any{"name": normalized.Name, "description": normalized.Description, "ingress_policy": normalized.IngressPolicy, "egress_policy": normalized.EgressPolicy}).Error; err != nil {
			return err
		}
		return markBoundFirewallsPending(tx, id)
	})
	if err != nil {
		return nil, err
	}
	if err = s.syncSecurityGroup(ctx, id); err != nil {
		return nil, err
	}
	return s.GetSecurityGroup(id)
}

func (s *VirtualisService) ReplaceSecurityGroupRules(ctx context.Context, id uint, inputs []FirewallInput) (*model.SecurityGroup, error) {
	if len(inputs) > 256 {
		return nil, BadRequest("每个安全组最多 256 条规则")
	}
	rules := make([]model.SecurityGroupRule, 0, len(inputs))
	for _, in := range inputs {
		r, err := normalizeFirewall(in)
		if err != nil {
			return nil, err
		}
		rules = append(rules, model.SecurityGroupRule{SecurityGroupID: id, Direction: r.Direction, Action: r.Action, Protocol: r.Protocol, PortStart: r.PortStart, PortEnd: r.PortEnd, CIDR: r.CIDR, Priority: r.Priority, Enabled: r.Enabled, Remark: r.Remark})
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := lockSecurityGroup(tx, id); err != nil {
			return err
		}
		if err := tx.Where("security_group_id = ?", id).Delete(&model.SecurityGroupRule{}).Error; err != nil {
			return err
		}
		if len(rules) > 0 {
			if err := tx.Create(&rules).Error; err != nil {
				return err
			}
		}
		return markBoundFirewallsPending(tx, id)
	})
	if err != nil {
		return nil, err
	}
	if err = s.syncSecurityGroup(ctx, id); err != nil {
		return nil, err
	}
	return s.GetSecurityGroup(id)
}

func (s *VirtualisService) DeleteSecurityGroup(id uint) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := lockSecurityGroup(tx, id); err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&model.InstanceSecurityGroup{}).Where("security_group_id = ?", id).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return Conflict("安全组仍绑定实例，不能删除")
		}
		if err := tx.Where("security_group_id = ?", id).Delete(&model.SecurityGroupRule{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.SecurityGroup{}, id).Error
	})
}
