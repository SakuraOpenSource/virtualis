package agentclient

import (
	"context"
	"errors"
)

// RequireFirewallPolicy 拒绝旧被控静默忽略默认拒绝策略；不影响旧规则接口。
func (c *Client) RequireFirewallPolicy(ctx context.Context, instance Instance) error {
	if instance.FirewallPolicy == nil {
		return nil
	}
	drivers, err := c.Drivers(ctx)
	if err != nil {
		return err
	}
	for _, driver := range drivers {
		if driver.Available && driver.FirewallPolicy && (instance.Driver == "" || instance.Driver == "auto" || instance.Driver == driver.Name) {
			return nil
		}
	}
	return errors.New("被控驱动不支持 firewall_policy，请升级后重试")
}
