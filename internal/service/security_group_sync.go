package service

import (
	"context"
	"fmt"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"gorm.io/gorm"
	"strings"
)

func markFirewallPending(tx *gorm.DB, id uint) error {
	return tx.Model(&model.Instance{}).Where("id = ?", id).Updates(map[string]any{"firewall_pending": true, "firewall_revision": gorm.Expr("firewall_revision + 1")}).Error
}

func markBoundFirewallsPending(tx *gorm.DB, groupID uint) error {
	ids := tx.Model(&model.InstanceSecurityGroup{}).Select("instance_id").Where("security_group_id = ?", groupID)
	return tx.Model(&model.Instance{}).Where("id IN (?)", ids).Updates(map[string]any{"firewall_pending": true, "firewall_revision": gorm.Expr("firewall_revision + 1")}).Error
}

func (s *VirtualisService) firewallSyncFailed(id uint, revision uint64, cause error) error {
	message := fmt.Sprintf("规则已保存，等待对账重试；当前下发失败：%s", cause)
	if err := s.db.Model(&model.Instance{}).Where("id = ? AND firewall_revision = ?", id, revision).Updates(map[string]any{"firewall_pending": true, "firewall_error": message}).Error; err != nil {
		return err
	}
	return Conflict("%s", message)
}

// syncSecurityGroup 每个绑定独立加生命周期锁；单个失败不得中止其它实例下发。
func (s *VirtualisService) syncSecurityGroup(ctx context.Context, id uint) error {
	var bindings []model.InstanceSecurityGroup
	if err := s.db.Where("security_group_id = ?", id).Order("instance_id ASC").Find(&bindings).Error; err != nil {
		return err
	}
	failures := []string{}
	for _, binding := range bindings {
		inst, err := s.GetAnyInstance(binding.InstanceID)
		if err != nil {
			failures = append(failures, fmt.Sprintf("实例 %d：%s", binding.InstanceID, err))
			continue
		}
		if inst.TrashedAt != nil || inst.Status != model.InstanceStatusRunning {
			continue
		}
		guard, err := s.beginOperation(ctx, inst.ID, "firewall_sync")
		if err != nil {
			err = s.firewallSyncFailed(inst.ID, inst.FirewallRevision, err)
		} else {
			err = s.syncFirewallIfRunning(ctx, inst.ID)
			guard.finish(&err)
		}
		if err != nil {
			failures = append(failures, fmt.Sprintf("实例 %d：%s", inst.ID, err))
		}
	}
	if len(failures) > 0 {
		return Conflict("安全组已保存，部分实例等待重试：%s", strings.Join(failures, "；"))
	}
	return nil
}
