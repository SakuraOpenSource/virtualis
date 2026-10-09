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

func TestMigrationImportsVerifiesSwitchesThenDeletesSource(t *testing.T) {
	var f lifecycleFixture
	events := []string{}
	f = newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/export"):
			events = append(events, "export")
			fmt.Fprint(w, "archive")
		case r.Method == "DELETE":
			var inst model.Instance
			if err := f.db.First(&inst, f.inst.ID).Error; err != nil {
				t.Error(err)
			}
			if *inst.AgentID == f.agent.ID {
				t.Error("source deleted before database switched")
			}
			events = append(events, "delete")
			w.WriteHeader(204)
		default:
			replyLifecycle(w, decodeLifecycleRequest(t, r))
		}
	})
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/drivers":
			fmt.Fprint(w, `{"items":[{"name":"qemu","available":true}]}`)
		case strings.HasSuffix(r.URL.Path, "/import"):
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
			}
			defer r.MultipartForm.RemoveAll()
			var inst agentclient.Instance
			if err := json.Unmarshal([]byte(r.FormValue("instance")), &inst); err != nil {
				t.Error(err)
			}
			if inst.RootPassword != "" || r.URL.RawQuery != "" {
				t.Error("unsafe migration import")
			}
			events = append(events, "import")
			inst.Status = "stopped"
			replyLifecycle(w, inst)
		default:
			events = append(events, "verify")
			replyLifecycle(w, decodeLifecycleRequest(t, r))
		}
	}))
	defer target.Close()
	agent := model.Agent{Name: "target", Status: "online", Endpoint: target.URL, TokenHash: "hash", Arch: "amd64"}
	if err := f.db.Create(&agent).Error; err != nil {
		t.Fatal(err)
	}
	NewAgentService(f.db).SeedRPCToken(agent.ID, "test-token")
	snap := model.Snapshot{InstanceID: f.inst.ID, AgentID: f.agent.ID, Name: "base", Status: "available"}
	f.db.Create(&snap)
	migrated, err := f.svc.MigrateInstance(context.Background(), f.inst.ID, MigrationInput{TargetAgentID: agent.ID})
	if err != nil {
		t.Fatal(err)
	}
	if *migrated.AgentID != agent.ID || migrated.BusyOperation != "" || migrated.Status != "stopped" {
		t.Fatalf("migration: %+v", migrated)
	}
	if strings.Join(events, ",") != "export,import,verify,delete" {
		t.Fatalf("order %v", events)
	}
	if err = f.db.First(&snap, snap.ID).Error; err != nil || snap.AgentID != agent.ID {
		t.Fatalf("snapshot ownership %+v %v", snap, err)
	}
}
