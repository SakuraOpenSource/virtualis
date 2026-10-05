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

func TestVPCValidationAndSmallSubnet(t *testing.T) {
	req := VPCInput{Name: "private-net", Subnet: "10.90.0.3/29", Gateway: "10.90.0.1"}
	vpc, err := normalizeVPC(req)
	if err != nil || vpc.Subnet != "10.90.0.0/29" || vpc.DHCPStart != "10.90.0.2" || vpc.DHCPEnd != "10.90.0.6" {
		t.Fatalf("vpc=%+v err=%v", vpc, err)
	}
	for _, invalid := range []VPCInput{
		{Name: "bad_name", Subnet: "10.0.0.0/24", Gateway: "10.0.0.1"},
		{Name: "net", Subnet: "10.0.0.0/30", Gateway: "10.0.0.1"},
		{Name: "net", Subnet: "10.0.0.0/24", Gateway: "10.1.0.1"},
		{Name: "net", Subnet: "10.0.0.0/24", Gateway: "10.0.0.0"},
		{Name: "net", Subnet: "10.0.0.0/24", Gateway: "10.0.0.1", DHCPStart: "10.0.0.1", DHCPEnd: "10.0.0.9"},
	} {
		if _, err := normalizeVPC(invalid); err == nil {
			t.Fatalf("accepted %+v", invalid)
		}
	}
}

func TestVPCCreateDeleteAndReferences(t *testing.T) {
	db := newIPPoolTestDB(t)
	agent := seedIPPoolAgent(t, db)
	created, deleted := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Agent-Token") != "test-token" {
			t.Error("missing authentication")
		}
		switch r.Method {
		case http.MethodPost:
			var spec agentclient.NetworkSpec
			if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
				t.Error(err)
			}
			if spec.NAT || spec.Name != "private-net" {
				t.Errorf("incorrect spec: %+v", spec)
			}
			created++
			w.Write([]byte(`{"name":"private-net","driver":"incus"}`))
		case http.MethodDelete:
			if r.URL.Path != "/api/vpc/private-net" || r.URL.Query().Get("driver") != "incus" {
				t.Error(r.URL)
			}
			deleted++
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	db.Model(&agent).Update("endpoint", server.URL)
	svc := NewVirtualisService(db)
	nat := false
	vpc, err := svc.CreateVPC(context.Background(), VPCInput{AgentID: agent.ID, Name: "private-net", Subnet: "10.90.0.0/24", Gateway: "10.90.0.1", NAT: &nat})
	if err != nil {
		t.Fatal(err)
	}
	if saved, err := svc.GetVPC(vpc.ID); err != nil || saved.NAT {
		t.Fatalf("false NAT not preserved: %+v, %v", saved, err)
	}
	inst := model.Instance{Name: "vpc-guest", VPCID: &vpc.ID, AgentID: &agent.ID}
	if err := db.Create(&inst).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteVPC(context.Background(), vpc.ID); err == nil {
		t.Fatal("deleted referenced network")
	}
	items, err := svc.ListVPCs(agent.ID)
	if err != nil || len(items) != 1 || items[0].InstanceCount != 1 {
		t.Fatalf("list: %+v %v", items, err)
	}
	db.Delete(&inst)
	if err := svc.DeleteVPC(context.Background(), vpc.ID); err != nil {
		t.Fatal(err)
	}
	if created != 1 || deleted != 1 {
		t.Fatalf("agent calls create=%d delete=%d", created, deleted)
	}
}
