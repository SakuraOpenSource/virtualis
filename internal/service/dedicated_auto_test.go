package service

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestDedicatedCreateAutomaticallyReservesPoolWithOneHostIP(t *testing.T) {
	db := newIPPoolTestDB(t)
	agent := seedIPPoolAgent(t, db)
	var got model.NetworkConfig
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/drivers":
			json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{{"name": "qemu", "available": true}}})
		case "/api/host/network":
			json.NewEncoder(w).Encode(map[string]any{"network": map[string]any{"ipv4_count": 1, "interfaces": []map[string]any{{"name": "eth0", "kind": "physical", "state": "up", "ipv4": []string{"192.0.2.1/24"}}}}})
		case "/api/instances":
			inst := decodeLifecycleRequest(t, r)
			got = inst.Network
			inst.Status = "stopped"
			replyLifecycle(w, inst)
		default:
			t.Errorf("unexpected remote path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer remote.Close()
	db.Model(&agent).Update("endpoint", remote.URL)
	svc := NewVirtualisService(db)
	if _, err := svc.SaveIPPoolDefaults(agent.ID, IPPoolInput{Gateway: "192.0.2.1", Prefix: 24, Interface: "eth0"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddIPPoolEntries(agent.ID, AddIPPoolEntriesInput{IPs: []string{"192.0.2.10"}}); err != nil {
		t.Fatal(err)
	}
	no := false
	inst, err := svc.CreateInstance(context.Background(), CreateInstanceRequest{Name: "auto-ip", Driver: "qemu", AgentID: &agent.ID, Network: model.NetworkConfig{Mode: "dedicated"}, AutoPassword: &no})
	if err != nil {
		t.Fatalf("automatic create: %v", err)
	}
	if got.IPv4 != "192.0.2.10/24" || got.Bridge != "eth0" || got.Gateway != "192.0.2.1" {
		t.Fatalf("wrong effective network: %+v", got)
	}
	var entry model.IPPoolEntry
	if err := db.Where("instance_id = ?", inst.ID).First(&entry).Error; err != nil {
		t.Fatal(err)
	}
	if entry.Status != "assigned" {
		t.Fatalf("reservation lost: %+v", entry)
	}
}

func TestDedicatedAutoConcurrentCreatesGetDistinctIPs(t *testing.T) {
	db := newIPPoolTestDB(t)
	agent := seedIPPoolAgent(t, db)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/drivers":
			json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{{"name": "qemu", "available": true}}})
		case "/api/host/network":
			json.NewEncoder(w).Encode(map[string]any{"network": map[string]any{"interfaces": []map[string]any{{"name": "eth0", "kind": "physical", "state": "up", "ipv4": []string{"192.0.2.1/24"}}}}})
		case "/api/instances":
			inst := decodeLifecycleRequest(t, r)
			inst.Status = "stopped"
			replyLifecycle(w, inst)
		default:
			w.WriteHeader(404)
		}
	}))
	defer remote.Close()
	db.Model(&agent).Update("endpoint", remote.URL)
	svc := NewVirtualisService(db)
	if _, err := svc.SaveIPPoolDefaults(agent.ID, IPPoolInput{Gateway: "192.0.2.1", Prefix: 24, Interface: "eth0"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddIPPoolEntries(agent.ID, AddIPPoolEntriesInput{IPs: []string{"192.0.2.10-192.0.2.25"}}); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errors := make(chan error, 16)
	ips := make(chan string, 16)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			no := false
			inst, err := NewVirtualisService(db).CreateInstance(context.Background(), CreateInstanceRequest{Name: fmt.Sprintf("concurrent-%d", i), Driver: "qemu", AgentID: &agent.ID, Network: model.NetworkConfig{Mode: "dedicated"}, AutoPassword: &no})
			if err != nil {
				errors <- err
				return
			}
			ips <- inst.Network.IPv4
		}(i)
	}
	close(start)
	wg.Wait()
	close(errors)
	close(ips)
	for err := range errors {
		t.Error(err)
	}
	seen := map[string]bool{}
	for ip := range ips {
		if seen[ip] {
			t.Errorf("duplicate allocation %s", ip)
		}
		seen[ip] = true
	}
	if len(seen) != 16 {
		t.Fatalf("successful unique allocations=%d, want 16", len(seen))
	}
	var assigned int64
	db.Model(&model.IPPoolEntry{}).Where("status = ?", "assigned").Count(&assigned)
	if assigned != 16 {
		t.Fatalf("reserved=%d", assigned)
	}
}
