package service

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestCreateBindsSecurityGroupsBeforeProvisionAndRejectsOldAgent(t *testing.T) {
	db := newIPPoolTestDB(t)
	agent := seedIPPoolAgent(t, db)
	svc := NewVirtualisService(db)
	g, err := svc.CreateSecurityGroup(SecurityGroupInput{Name: "create-drop"})
	if err != nil {
		t.Fatal(err)
	}
	var policyReady atomic.Bool
	policyReady.Store(true)
	var creates atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/drivers" {
			json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{{"name": "qemu", "available": true, "firewall_policy": policyReady.Load()}}})
			return
		}
		if r.URL.Path == "/api/instances" {
			creates.Add(1)
			inst := decodeLifecycleRequest(t, r)
			var count int64
			db.Model(&model.InstanceSecurityGroup{}).Where("instance_id = ? AND security_group_id = ?", inst.ID, g.ID).Count(&count)
			if count != 1 || inst.FirewallPolicy == nil || inst.FirewallPolicy.Ingress != "drop" {
				t.Errorf("unprotected create: bindings=%d policy=%+v", count, inst.FirewallPolicy)
			}
			inst.Status = "stopped"
			replyLifecycle(w, inst)
			return
		}
		w.WriteHeader(404)
	}))
	defer remote.Close()
	db.Model(&agent).Update("endpoint", remote.URL)
	var req CreateInstanceRequest
	json.Unmarshal([]byte(fmt.Sprintf(`{"name":"sg-create","driver":"qemu","agent_id":%d,"network":{"mode":"nat"},"auto_password":false,"security_group_ids":[%d]}`, agent.ID, g.ID)), &req)
	inst, err := svc.CreateInstance(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := svc.GetAnyInstance(inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.SecurityGroups) != 1 || toWireInstance(loaded, nil).FirewallPolicy == nil {
		t.Fatal("group not loaded on lifecycle wire")
	}
	policyReady.Store(false)
	req.Name = "sg-old-agent"
	if _, err = svc.CreateInstance(context.Background(), req); err == nil {
		t.Fatal("old agent created protected instance")
	}
	if creates.Load() != 1 {
		t.Fatal("unsupported agent reached remote create")
	}
	var count int64
	db.Model(&model.Instance{}).Where("name = ?", req.Name).Count(&count)
	if count != 0 {
		t.Fatal("unsupported create left instance")
	}
}
