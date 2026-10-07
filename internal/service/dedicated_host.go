package service

import (
	"github.com/SakuraOpenSource/virtualis/internal/agentclient"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"net"
	"strings"
)

// validateDedicatedHost 检查实际可用上联和地址冲突，不把主机地址数当作池容量。
func validateDedicatedHost(network model.NetworkConfig, host *agentclient.HostNetworkSummary) error {
	if host == nil {
		return Conflict("无法读取被控网络")
	}
	ip := net.ParseIP(primaryConfiguredIP(network))
	if ip == nil || ip.To4() == nil || !ip.IsGlobalUnicast() {
		return BadRequest("独立 IPv4 必须是有效单播地址")
	}
	var uplink *agentclient.HostInterface
	for i := range host.Interfaces {
		iface := &host.Interfaces[i]
		for _, address := range iface.IPv4 {
			if sameIPv4(address, ip.String()) {
				return Conflict("独立 IPv4 已分配给被控主机")
			}
		}
		if iface.Name == "lo" || (iface.Kind != "physical" && iface.Kind != "bridge" && iface.Kind != "vlan") || strings.EqualFold(iface.State, "down") {
			continue
		}
		if network.Bridge == iface.Name || (network.Bridge == "" && uplink == nil) {
			uplink = iface
		}
	}
	if uplink == nil {
		return BadRequest("没有可用的独立 IP 上联网卡")
	}
	if network.DedicatedMode == "bridge" && uplink.Kind != "bridge" {
		return BadRequest("bridge 接入只能挂载已有 Linux 网桥")
	}
	if network.Gateway != "" && sameIPv4(network.Gateway, ip.String()) {
		return BadRequest("实例 IPv4 不能与网关相同")
	}
	return nil
}
