package model

import "testing"

func TestSSHPasswordRoundTrip(t *testing.T) {
	var inst Instance
	if got := inst.LoadSSHPassword(); got != "" {
		t.Fatalf("空配置应返回空密码，实际 %q", got)
	}
	inst.StoreSSHPassword("s3cret-pw")
	if inst.ConfigJSON == "" {
		t.Fatal("密码未写入 ConfigJSON")
	}
	if got := inst.LoadSSHPassword(); got != "s3cret-pw" {
		t.Fatalf("读回密码不符，实际 %q", got)
	}
	// 坏 JSON 不应 panic。
	inst.ConfigJSON = "{bad"
	if inst.LoadSSHPassword() != "" {
		t.Fatal("坏 JSON 应返回空密码")
	}
}

func TestValidNATProtocol(t *testing.T) {
	for _, p := range []string{"tcp", "udp"} {
		if !ValidNATProtocol(p) {
			t.Errorf("%s 应合法", p)
		}
	}
	for _, p := range []string{"", "icmp", "TCP "} {
		if ValidNATProtocol(p) {
			t.Errorf("%q 应非法", p)
		}
	}
}
