package service

import (
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"net"
)

func validObservedIPv4(raw string) string {
	ip := net.ParseIP(raw)
	if ip == nil || ip.To4() == nil || !ip.IsGlobalUnicast() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return ""
	}
	return ip.String()
}
func instanceDisplayIP(inst *model.Instance) string {
	if ip := validObservedIPv4(inst.ObservedIP); ip != "" {
		return ip
	}
	return primaryConfiguredIP(inst.Network)
}
