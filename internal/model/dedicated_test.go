package model

import (
	"encoding/json"
	"testing"
)

func TestDedicatedModePreservesWireAndRejectsUnknown(t *testing.T) {
	var network NetworkConfig
	if err := json.Unmarshal([]byte(`{"mode":"dedicated","dedicated_mode":"routed","ipv4":"192.0.2.10/24","gateway":"192.0.2.1"}`), &network); err != nil {
		t.Fatal(err)
	}
	normalized, err := NormalizeNetworkConfig(network)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(normalized)
	var wire map[string]any
	json.Unmarshal(raw, &wire)
	if wire["dedicated_mode"] != "routed" || wire["ipv4"] != "192.0.2.10/24" {
		t.Fatalf("desired wire lost: %s", raw)
	}
	json.Unmarshal([]byte(`{"mode":"dedicated","dedicated_mode":"unsafe","ipv4":"192.0.2.10/24"}`), &network)
	if _, err = NormalizeNetworkConfig(network); err == nil {
		t.Fatal("unknown dedicated mode accepted")
	}
	for _, ip := range []string{"0.0.0.0", "127.0.0.1", "224.0.0.1", "255.255.255.255"} {
		network = NetworkConfig{Mode: "dedicated", IPv4: ip}
		if _, err = NormalizeNetworkConfig(network); err == nil {
			t.Errorf("non-unicast dedicated IP accepted: %s", ip)
		}
	}
}
