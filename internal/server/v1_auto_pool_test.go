package server

import (
	"encoding/json"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"github.com/SakuraOpenSource/virtualis/internal/service"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestV1AutoAgentSelectionSkipsNodesWithoutFreePool(t *testing.T) {
	f := newAPIFixture(t)
	seenAt := time.Now().UTC()
	emptyAgent := model.Agent{Name: "nat-only", Status: "online", Endpoint: "http://127.0.0.1:9", TokenHash: "h", LastSeenAt: &seenAt}
	f.db.Create(&emptyAgent)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/drivers":
			json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{{"name": "qemu", "available": true}}})
		case "/api/host/network":
			json.NewEncoder(w).Encode(map[string]any{"network": map[string]any{"interfaces": []map[string]any{{"name": "eth0", "kind": "physical", "state": "up", "ipv4": []string{"198.51.100.1/24"}}}}})
		case "/api/instances":
			var payload struct {
				Instance model.Instance `json:"instance"`
			}
			json.NewDecoder(r.Body).Decode(&payload)
			if payload.Instance.Network.Mode != "dedicated" || payload.Instance.Network.IPv4 == "" {
				t.Errorf("auto create missing pool address: %+v", payload.Instance.Network)
			}
			payload.Instance.Status = "stopped"
			json.NewEncoder(w).Encode(map[string]any{"instance": payload.Instance})
		default:
			t.Errorf("unexpected: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer remote.Close()
	poolAgent := model.Agent{Name: "with-pool", Status: "online", Endpoint: remote.URL, TokenHash: "h", LastSeenAt: &seenAt}
	f.db.Create(&poolAgent)
	// Plaintext tokens live in the process cache, not the database (SC-03).
	service.NewAgentService(f.db).SeedRPCToken(poolAgent.ID, "test-token")
	svc := service.NewVirtualisService(f.db)
	svc.SaveIPPoolDefaults(poolAgent.ID, service.IPPoolInput{Gateway: "198.51.100.1", Prefix: 24, Interface: "eth0"})
	svc.AddIPPoolEntries(poolAgent.ID, service.AddIPPoolEntriesInput{IPs: []string{"198.51.100.10"}})
	res := f.request(t, "POST", "/api/v1/instances", `{"name":"v1-auto","driver":"qemu","network":{"mode":"dedicated"},"auto_password":false}`, f.key, nil)
	if res.Code != 200 {
		t.Fatalf("auto create: %d %s", res.Code, res.Body.String())
	}
	var inst model.Instance
	json.Unmarshal(res.Body.Bytes(), &inst)
	if inst.AgentID == nil || *inst.AgentID != poolAgent.ID {
		t.Fatalf("picked pool-less node: %+v", inst.AgentID)
	}
	if inst.Network.IPv4 == "" {
		t.Fatal("auto allocation missing")
	}
	res = f.request(t, "POST", "/api/v1/instances", `{"name":"v1-empty","driver":"qemu","network":{"mode":"dedicated"},"auto_password":false}`, f.key, nil)
	if res.Code != 409 {
		t.Fatalf("pool exhaustion should conflict: %d %s", res.Code, res.Body.String())
	}
}
