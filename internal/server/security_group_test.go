package server

import (
	"encoding/json"
	"fmt"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSecurityGroupAdminCreateDefaults(t *testing.T) {
	f := newAPIFixture(t)
	res := f.request(t, "POST", "/api/admin/security-groups", `{"name":"web","description":"网站"}`, "", &f.admin)
	if res.Code != 200 && res.Code != 201 {
		t.Fatalf("create: %d %s", res.Code, res.Body.String())
	}
	var group struct {
		ID      uint   `json:"id"`
		Ingress string `json:"ingress_policy"`
		Egress  string `json:"egress_policy"`
		Rules   []any  `json:"rules"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &group); err != nil {
		t.Fatal(err)
	}
	if group.ID == 0 || group.Ingress != "drop" || group.Egress != "accept" || group.Rules == nil || len(group.Rules) != 0 {
		t.Fatalf("wrong default group: %s", res.Body.String())
	}
	res = f.request(t, "GET", "/api/admin/security-groups", "", "", &f.admin)
	if res.Code != 200 {
		t.Fatalf("list: %d %s", res.Code, res.Body.String())
	}
}

func TestSecurityGroupBindEmptyIngressSendsDropAndKeepsPrivateRules(t *testing.T) {
	f := newAPIFixture(t)
	var payload map[string]any
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/drivers" {
			json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{{"name": "qemu", "available": true, "firewall_policy": true}}})
			return
		}
		if r.URL.Path != fmt.Sprintf("/api/instances/%d/firewall", f.inst.ID) {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		w.WriteHeader(204)
	}))
	defer remote.Close()
	agent := model.Agent{Name: "sg-agent", Endpoint: remote.URL, Token: "test-token", TokenHash: "test", Status: "online"}
	if err := f.db.Create(&agent).Error; err != nil {
		t.Fatal(err)
	}
	f.db.Model(&f.inst).Updates(map[string]any{"agent_id": agent.ID, "status": "running"})
	res := f.request(t, "POST", "/api/admin/security-groups", `{"name":"empty"}`, "", &f.admin)
	var group struct {
		ID uint `json:"id"`
	}
	json.Unmarshal(res.Body.Bytes(), &group)
	path := fmt.Sprintf("/api/instances/%d/security-groups", f.inst.ID)
	res = f.request(t, "PUT", path, fmt.Sprintf(`{"security_group_ids":[%d]}`, group.ID), "", &f.admin)
	if res.Code != 200 {
		t.Fatalf("bind: %d %s", res.Code, res.Body.String())
	}
	instance, ok := payload["instance"].(map[string]any)
	if !ok {
		t.Fatalf("missing applied instance: %#v", payload)
	}
	policy, ok := instance["firewall_policy"].(map[string]any)
	if !ok || policy["ingress"] != "drop" || policy["egress"] != "accept" {
		t.Fatalf("empty group did not enforce drop: %#v", instance)
	}
	res = f.request(t, "GET", path, "", "", &f.owner)
	if res.Code != 200 {
		t.Fatalf("owner read: %d %s", res.Code, res.Body.String())
	}
	var binding map[string]any
	json.Unmarshal(res.Body.Bytes(), &binding)
	for _, key := range []string{"security_group_ids", "groups", "effective_rules", "firewall_policy"} {
		if binding[key] == nil {
			t.Fatalf("missing %s: %s", key, res.Body.String())
		}
	}
	res = f.request(t, "GET", fmt.Sprintf("/api/v1/instances/%d/firewall", f.inst.ID), "", f.key, nil)
	if res.Code != 200 {
		t.Fatalf("legacy firewall read: %d %s", res.Code, res.Body.String())
	}
}
