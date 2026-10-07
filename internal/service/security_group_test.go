package service

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"net/http"
	"sync"
	"testing"
)

func TestSecurityGroupRulesFanoutPersistsRetryAndMergesDeterministically(t *testing.T) {
	var mu sync.Mutex
	calls := map[uint]int{}
	fail := true
	f := newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/drivers" {
			json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{{"name": "qemu", "available": true, "firewall_policy": true}}})
			return
		}
		inst := decodeLifecycleRequest(t, r)
		mu.Lock()
		calls[inst.ID]++
		shouldFail := fail && inst.ID == 1
		mu.Unlock()
		if shouldFail {
			http.Error(w, "fixture atomic replace failed", 502)
			return
		}
		w.WriteHeader(204)
	})
	raw, _ := f.db.DB()
	raw.SetMaxOpenConns(1)
	second := model.Instance{Name: "second-sg", Driver: "qemu", Status: "running", AgentID: &f.agent.ID}
	if err := f.db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	f.db.Model(&f.inst).Update("status", "running")
	g, err := f.svc.CreateSecurityGroup(SecurityGroupInput{Name: "shared"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint{f.inst.ID, second.ID} {
		if err := f.db.Create(&model.InstanceSecurityGroup{InstanceID: id, SecurityGroupID: g.ID}).Error; err != nil {
			t.Fatal(err)
		}
	}
	local := model.FirewallRule{InstanceID: f.inst.ID, AgentID: f.agent.ID, Direction: "out", Action: "drop", Protocol: "any", Priority: 3, Enabled: true, Remark: "local"}
	f.db.Create(&local)
	no := false
	inputs := []FirewallInput{{Direction: "in", Action: "accept", Protocol: "tcp", PortStart: 443, Priority: 2, Remark: "group"}, {Direction: "in", Action: "accept", Protocol: "tcp", PortStart: 22, Priority: 1, Enabled: &no}}
	_, err = f.svc.ReplaceSecurityGroupRules(context.Background(), g.ID, inputs)
	if err == nil {
		t.Fatal("fanout failure reported success")
	}
	mu.Lock()
	a, b := calls[f.inst.ID], calls[second.ID]
	mu.Unlock()
	if a != 1 || b != 1 {
		t.Fatalf("fanout stopped at failure: %v", calls)
	}
	inst, err := f.svc.GetInstance(f.inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !inst.FirewallPending || inst.FirewallError == "" {
		t.Fatalf("missing persistent retry state: pending=%v error=%q", inst.FirewallPending, inst.FirewallError)
	}
	view, err := f.svc.InstanceSecurityGroups(f.inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.EffectiveRules) != 2 || view.EffectiveRules[0].Remark != "group" || view.EffectiveRules[1].Remark != "local" || view.FirewallPolicy.Ingress != "drop" {
		t.Fatalf("incorrect effective merge: %+v", view)
	}
	if len(inst.FirewallRules) != 1 {
		t.Fatal("group rules leaked into local list")
	}
	mu.Lock()
	fail = false
	mu.Unlock()
	if _, err = f.svc.ReplaceSecurityGroupRules(context.Background(), g.ID, inputs); err != nil {
		t.Fatalf("retry: %v", err)
	}
	inst, _ = f.svc.GetInstance(f.inst.ID)
	if inst.FirewallPending || inst.FirewallError != "" || inst.FirewallAppliedRevision != inst.FirewallRevision {
		t.Fatalf("retry not acknowledged: %s", fmt.Sprint(inst.FirewallPending, inst.FirewallError, inst.FirewallAppliedRevision, inst.FirewallRevision))
	}
}
