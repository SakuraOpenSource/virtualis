package service

import (
	"context"
	"encoding/json"
	"github.com/SakuraOpenSource/virtualis/internal/agentclient"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"net/http"
	"strings"
	"testing"
)

func TestResizeRejectsInvalidResources(t *testing.T) {
	f := newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) { replyLifecycle(w, decodeLifecycleRequest(t, r)) })
	for _, spec := range []model.InstanceSpec{{CPU: -1, MemoryMB: 1024, DiskGB: 20}, {CPU: 1, MemoryMB: -1, DiskGB: 20}, {CPU: 1, CPUMilli: -1, MemoryMB: 1024, DiskGB: 20}} {
		if _, err := f.svc.ResizeInstance(context.Background(), f.inst.ID, ResizeInput{Spec: spec}); err == nil {
			t.Errorf("invalid resources accepted: %+v", spec)
		}
	}
}

func TestResizeStoppedPersistsPreservesNetworkAndIsIdempotent(t *testing.T) {
	calls := 0
	f := newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/resize") {
			calls++
			var p struct {
				Instance agentclient.Instance `json:"instance"`
				Spec     model.InstanceSpec   `json:"spec"`
				Network  model.NetworkConfig  `json:"network"`
			}
			if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
				t.Error(err)
			}
			if p.Instance.RootPassword != "" || p.Network.IPv4 != "10.0.0.2/24" || p.Network.BandwidthMbps != 50 {
				t.Errorf("unsafe resize payload: %+v", p)
			}
			p.Instance.Spec = p.Spec
			p.Instance.Network = p.Network
			replyLifecycle(w, p.Instance)
			return
		}
		replyLifecycle(w, decodeLifecycleRequest(t, r))
	})
	req := ResizeInput{Spec: model.InstanceSpec{CPU: 2, MemoryMB: 2048, DiskGB: 30}, Network: &model.NetworkConfig{BandwidthMbps: 50, TrafficGB: 100}}
	inst, err := f.svc.ResizeInstance(context.Background(), f.inst.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	if inst.Spec.CPU != 2 || inst.Spec.DiskGB != 30 || inst.Network.MAC != f.inst.Network.MAC || inst.Network.BandwidthMbps != 50 {
		t.Fatalf("resize: %+v", inst)
	}
	if _, err = f.svc.ResizeInstance(context.Background(), f.inst.ID, req); err != nil || calls != 1 {
		t.Fatalf("idempotency: calls=%d err=%v", calls, err)
	}
	req.Spec.DiskGB = 10
	if _, err = f.svc.ResizeInstance(context.Background(), f.inst.ID, req); err == nil {
		t.Fatal("shrink accepted")
	}
	req.Spec.DiskGB = 30
	req.Spec.Arch = "aarch64"
	if _, err = f.svc.ResizeInstance(context.Background(), f.inst.ID, req); err == nil {
		t.Fatal("architecture change accepted")
	}
}
