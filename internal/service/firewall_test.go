package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SakuraOpenSource/virtualis/internal/agentclient"
	"github.com/SakuraOpenSource/virtualis/internal/model"
)

func TestFirewallValidation(t *testing.T) {
	if rule, err := normalizeFirewall(FirewallInput{Direction: "in", Action: "drop", Protocol: "tcp", PortStart: 22, CIDR: "192.0.2.7"}); err != nil || rule.PortEnd != 22 || rule.CIDR != "192.0.2.7/32" {
		t.Fatalf("rule=%+v err=%v", rule, err)
	}
	for _, req := range []FirewallInput{
		{Direction: "sideways", Action: "drop", Protocol: "tcp"},
		{Direction: "in", Action: "drop", Protocol: "icmp", PortStart: 22},
		{Direction: "out", Action: "accept", Protocol: "tcp", PortStart: 100, PortEnd: 1},
		{Direction: "in", Action: "drop", Protocol: "any", CIDR: "::/0"},
	} {
		if _, err := normalizeFirewall(req); err == nil {
			t.Fatalf("accepted %+v", req)
		}
	}
}

func TestFirewallStoppedPersistsAndRunningSynchronizes(t *testing.T) {
	db := newIPPoolTestDB(t)
	agent := seedIPPoolAgent(t, db)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Instance agentclient.Instance       `json:"instance"`
			Rules    []agentclient.FirewallRule `json:"rules"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		calls++
		if len(payload.Rules) != 1 || payload.Rules[0].Enabled {
			t.Errorf("disabled rule lost: %+v", payload)
		}
		w.Write([]byte(`{}`))
	}))
	defer server.Close()
	db.Model(&agent).Update("endpoint", server.URL)
	inst := model.Instance{Name: "fw-guest", AgentID: &agent.ID, Status: model.InstanceStatusStopped}
	if err := db.Create(&inst).Error; err != nil {
		t.Fatal(err)
	}
	svc := NewVirtualisService(db)
	req := FirewallInput{Direction: "in", Action: "drop", Protocol: "tcp", PortStart: 22}
	rule, err := svc.CreateFirewall(context.Background(), inst.ID, req)
	if err != nil || calls != 0 {
		t.Fatalf("stopped: %v calls=%d", err, calls)
	}
	disabled := false
	req.Enabled = &disabled
	db.Model(&inst).Update("status", model.InstanceStatusRunning)
	if _, err := svc.UpdateFirewall(context.Background(), rule.ID, req); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("sync calls=%d", calls)
	}
	db.Model(&inst).Update("status", model.InstanceStatusStopped)
	if err := svc.DeleteFirewall(context.Background(), rule.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateFirewall(context.Background(), rule.ID, req); err == nil {
		t.Fatal("updated deleted rule")
	}
}
