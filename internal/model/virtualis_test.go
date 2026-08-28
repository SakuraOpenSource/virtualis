package model

import "testing"

func TestNormalizeNetworkConfigModes(t *testing.T) {
	// 历史数据 bridge 归一化为 dedicated。
	n, err := NormalizeNetworkConfig(NetworkConfig{Mode: "bridge", Bridge: "br0"})
	if err != nil || n.Mode != NetworkModeDedicated {
		t.Fatalf("bridge 应归一化为 dedicated，实际 %s (%v)", n.Mode, err)
	}
	if n, err = NormalizeNetworkConfig(NetworkConfig{}); err != nil || n.Mode != NetworkModeNAT {
		t.Fatalf("空模式应默认 nat，实际 %s (%v)", n.Mode, err)
	}
	if _, err = NormalizeNetworkConfig(NetworkConfig{Mode: "weird"}); err == nil {
		t.Fatal("未知模式应报错")
	}
}

func TestNormalizeNetworkConfigDedicated(t *testing.T) {
	n, err := NormalizeNetworkConfig(NetworkConfig{
		Mode: NetworkModeDedicated, Bridge: "eth0",
		IPv4: "192.168.1.20/24", Gateway: "192.168.1.1",
		DNS: []string{"1.1.1.1", "8.8.8.8"}, BandwidthMbps: 200,
	})
	if err != nil {
		t.Fatalf("合法独立 IP 配置不应报错: %v", err)
	}
	if n.Bridge != "eth0" || n.IPv4 != "192.168.1.20/24" {
		t.Fatalf("字段应原样保留: %+v", n)
	}
	// MAC 小写归一化。
	n, _ = NormalizeNetworkConfig(NetworkConfig{Mode: NetworkModeNAT, MAC: "52:54:00:AB:CD:EF"})
	if n.MAC != "52:54:00:ab:cd:ef" {
		t.Fatalf("MAC 应小写: %s", n.MAC)
	}
	// 非法 MAC。
	if _, err = NormalizeNetworkConfig(NetworkConfig{MAC: "not-a-mac"}); err == nil {
		t.Fatal("非法 MAC 应报错")
	}
}

func TestValidDriverWithoutMock(t *testing.T) {
	for _, d := range []string{"auto", "qemu", "incus", "lxc"} {
		if !ValidDriver(d) {
			t.Errorf("%s 应为合法驱动", d)
		}
	}
	if ValidDriver("mock") {
		t.Error("mock 已移除，不应再是合法驱动")
	}
	for _, d := range AllDrivers() {
		if !ValidDriver(d) {
			t.Errorf("AllDrivers 里的 %s 应合法", d)
		}
	}
}
