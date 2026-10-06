package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SakuraOpenSource/virtualis/internal/agentclient"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"github.com/SakuraOpenSource/virtualis/internal/storage"
	"gorm.io/gorm"
)

type lifecycleFixture struct {
	db    *gorm.DB
	svc   *VirtualisService
	inst  model.Instance
	agent model.Agent
}

func newLifecycleFixture(t *testing.T, fn http.HandlerFunc) lifecycleFixture {
	t.Helper()
	db := newIPPoolTestDB(t)
	server := httptest.NewServer(fn)
	t.Cleanup(server.Close)
	agent := seedIPPoolAgent(t, db)
	if err := db.Model(&agent).Updates(map[string]any{"endpoint": server.URL, "arch": "amd64"}).Error; err != nil {
		t.Fatal(err)
	}
	inst := model.Instance{Name: "life-guest", Driver: model.DriverQEMU, Type: model.InstanceTypeVM, AgentID: &agent.ID, Status: model.InstanceStatusStopped, Spec: model.InstanceSpec{CPU: 1, MemoryMB: 1024, DiskGB: 20, Arch: "x86_64"}, Network: model.NetworkConfig{Mode: "nat", IPv4: "10.0.0.2/24", MAC: "52:54:00:00:00:02"}}
	if err := db.Create(&inst).Error; err != nil {
		t.Fatal(err)
	}
	return lifecycleFixture{db: db, svc: NewVirtualisService(db, storage.New(t.TempDir())), inst: inst, agent: agent}
}
func decodeLifecycleRequest(t *testing.T, r *http.Request) agentclient.Instance {
	t.Helper()
	if r.Header.Get("X-Agent-Token") != "test-token" {
		t.Error("missing agent authentication")
	}
	var payload struct {
		Instance agentclient.Instance `json:"instance"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		t.Error(err)
	}
	return payload.Instance
}
func replyLifecycle(w http.ResponseWriter, inst agentclient.Instance) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"instance": inst})
}

func TestLifecycleStatusNetworkAndPasswordRespectDatabaseFence(t *testing.T) {
	f := newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("busy request reached Agent: %s", r.URL.Path)
		replyLifecycle(w, decodeLifecycleRequest(t, r))
	})
	if err := f.db.Model(&f.inst).Update("busy_operation", "foreign-worker").Error; err != nil {
		t.Fatal(err)
	}
	other := NewVirtualisService(f.db)
	for name, call := range map[string]func() error{
		"status":  func() error { _, err := other.RefreshStatus(context.Background(), f.inst.ID); return err },
		"network": func() error { _, err := other.InstanceNetwork(context.Background(), f.inst.ID); return err },
		"configure": func() error {
			_, _, err := other.ConfigureInstanceNetwork(context.Background(), f.inst.ID, f.inst.Network)
			return err
		},
		"password": func() error {
			_, err := other.SetInstancePassword(context.Background(), f.inst.ID, "new-password")
			return err
		},
		"nat": func() error {
			_, err := other.CreateNATMapping(context.Background(), f.inst.ID, CreateNATMappingRequest{GuestPort: 22})
			return err
		},
		"firewall": func() error {
			_, err := other.CreateFirewall(context.Background(), f.inst.ID, FirewallInput{Direction: "in", Action: "drop", Protocol: "any"})
			return err
		},
	} {
		if err := call(); err == nil {
			t.Errorf("%s ignored database fence", name)
		}
	}
}

func TestLifecycleConflictingPower(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	f := newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		inst := decodeLifecycleRequest(t, r)
		if strings.HasSuffix(r.URL.Path, "/power") && calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		inst.Status = model.InstanceStatusStopped
		replyLifecycle(w, inst)
	})
	done := make(chan error, 1)
	go func() { _, err := f.svc.PowerInstance(context.Background(), f.inst.ID, "stop"); done <- err }()
	<-entered
	_, err := f.svc.PowerInstance(context.Background(), f.inst.ID, "start")
	close(release)
	if first := <-done; first != nil {
		t.Fatal(first)
	}
	if err == nil {
		t.Fatal("concurrent power succeeded; expected busy conflict")
	}
	if be, ok := AsError(err); !ok || be.Status != 409 {
		t.Fatalf("want 409, got %v", err)
	}
}
