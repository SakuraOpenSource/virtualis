package service

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/SakuraOpenSource/virtualis/internal/agentclient"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDedicatedMigrationAcceptsSingleIPUplinkAndPreservesDesiredCIDR(t *testing.T) {
	f := newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/export") {
			fmt.Fprint(w, "fixture archive")
			return
		}
		if r.Method == "DELETE" {
			w.WriteHeader(204)
			return
		}
		replyLifecycle(w, decodeLifecycleRequest(t, r))
	})
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/drivers":
			fmt.Fprint(w, `{"items":[{"name":"qemu","available":true}]}`)
		case r.URL.Path == "/api/host/network":
			fmt.Fprint(w, `{"network":{"ipv4_count":1,"interfaces":[{"name":"eth0","kind":"physical","state":"up","ipv4":["198.51.100.1/24"]}]}}`)
		case strings.HasSuffix(r.URL.Path, "/import"):
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
				return
			}
			defer r.MultipartForm.RemoveAll()
			var inst agentclient.Instance
			if err := json.Unmarshal([]byte(r.FormValue("instance")), &inst); err != nil {
				t.Error(err)
				return
			}
			if inst.Network.IPv4 != "198.51.100.10/24" {
				t.Errorf("desired CIDR lost: %+v", inst.Network)
			}
			inst.Status = "stopped"
			replyLifecycle(w, inst)
		default:
			replyLifecycle(w, decodeLifecycleRequest(t, r))
		}
	}))
	defer target.Close()
	agent := model.Agent{Name: "dedicated-target", Status: "online", Endpoint: target.URL, TokenHash: "test", Arch: "amd64"}
	f.db.Create(&agent)
	NewAgentService(f.db).SeedRPCToken(agent.ID, "test-token")
	f.svc.SaveIPPoolDefaults(agent.ID, IPPoolInput{Interface: "eth0", Prefix: 24, Gateway: "198.51.100.1"})
	f.svc.AddIPPoolEntries(agent.ID, AddIPPoolEntriesInput{IPs: []string{"198.51.100.10"}})
	entries, _ := f.svc.FreeIPPoolEntries(agent.ID)
	desired := model.NetworkConfig{Mode: "dedicated", DedicatedMode: "routed"}
	inst, err := f.svc.MigrateInstance(context.Background(), f.inst.ID, MigrationInput{TargetAgentID: agent.ID, IPPoolEntryID: &entries[0].ID, Network: &desired})
	if err != nil {
		t.Fatalf("dedicated migration: %v", err)
	}
	if *inst.AgentID != agent.ID || inst.Network.IPv4 != "198.51.100.10/24" || inst.Network.DedicatedMode != "routed" {
		t.Fatalf("wrong migration destination")
	}
}
