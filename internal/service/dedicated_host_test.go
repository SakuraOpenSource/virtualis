package service

import (
	"context"
	"encoding/json"
	"github.com/SakuraOpenSource/virtualis/internal/agentclient"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"net/http"
	"testing"
)

func TestDedicatedConfigureUsesUplinkNotHostIPv4Count(t *testing.T) {
	f := newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/host/network" {
			json.NewEncoder(w).Encode(map[string]any{"network": map[string]any{"ipv4_count": 1, "interfaces": []map[string]any{{"name": "eth0", "kind": "physical", "state": "up", "ipv4": []string{"192.0.2.1/24"}}}}})
			return
		}
		replyLifecycle(w, decodeLifecycleRequest(t, r))
	})
	_, _, err := f.svc.ConfigureInstanceNetwork(context.Background(), f.inst.ID, model.NetworkConfig{Mode: "dedicated", Bridge: "eth0", IPv4: "192.0.2.10/24", Gateway: "192.0.2.1"})
	if err != nil {
		t.Fatalf("one-IP uplink should configure: %v", err)
	}
}

func TestDedicatedRejectsPhysicalBridgeAttachmentAndHostCollision(t *testing.T) {
	host := &agentclient.HostNetworkSummary{Interfaces: []agentclient.HostInterface{{Name: "eth0", Kind: "physical", State: "up", IPv4: []string{"192.0.2.1/24"}}}}
	if err := validateDedicatedHost(model.NetworkConfig{Mode: "dedicated", DedicatedMode: "bridge", Bridge: "eth0", IPv4: "192.0.2.10/24"}, host); err == nil {
		t.Fatal("explicit bridge attached to physical uplink")
	}
	if err := validateDedicatedHost(model.NetworkConfig{Mode: "dedicated", Bridge: "eth0", IPv4: "192.0.2.1/24"}, host); err == nil {
		t.Fatal("host collision accepted")
	}
}
