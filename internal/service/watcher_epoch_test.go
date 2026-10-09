package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SakuraOpenSource/virtualis/internal/model"
)

// REV-WATCHER-EPOCH: a slow watchSSHReady status reply captured before a
// same-node reinstall must not mark the NEW guest SSH-ready. The old
// condition (busy_operation='' AND agent_id unchanged) is an ABA: after the
// reinstall completes and releases the fence, busy is '' again and the
// agent is the same, so the stale reply overwrites the new guest's
// ssh_ready=false with the OLD guest's true.
func TestWatcherDoesNotClobberReinstalledGuest(t *testing.T) {
	var mu sync.Mutex
	blockStatus := make(chan struct{})
	statusEntered := make(chan struct{})
	f := newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/status") {
			// Old watcher's Status RPC in flight.
			mu.Lock()
			entered := statusEntered
			mu.Unlock()
			if entered != nil {
				close(entered)
				mu.Lock()
				statusEntered = nil
				mu.Unlock()
				<-blockStatus
			}
			inst := decodeLifecycleRequest(t, r)
			inst.SSHReady = true
			inst.Status = model.InstanceStatusRunning
			replyLifecycle(w, inst)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/power") {
			var p struct {
				Action   string `json:"action"`
				Instance struct {
					Status string `json:"status"`
				} `json:"instance"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			st := p.Instance.Status
			if st == "" {
				st = model.InstanceStatusStopped
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"instance": map[string]any{"id": 0, "status": st}})
			return
		}
		replyLifecycle(w, decodeLifecycleRequest(t, r))
	})

	// Instance pretends first-boot injection is pending; the watcher will
	// poll the agent and the agent reports the OLD guest ready.
	if err := f.db.Model(&f.inst).Updates(map[string]any{"ssh_ready": false, "busy_operation": "", "status": model.InstanceStatusRunning}).Error; err != nil {
		t.Fatal(err)
	}

	// Start the watcher against the OLD guest.
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		f.svc.watchSSHReady(f.inst.ID, &f.agent)
	}()
	// Wait until the old watcher's Status RPC is in flight.
	<-statusEntered

	// Complete a same-node reinstall while the old reply is still pending.
	if err := f.db.Model(&f.inst).Update("ssh_ready", false).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.PowerInstance(context.Background(), f.inst.ID, model.ActionReinstall, nil); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	// The reinstall set ssh_ready=false for the NEW guest and released the
	// fence: busy='' and agent unchanged — the ABA window.
	inst, err := f.svc.GetAnyInstance(f.inst.ID)
	if err != nil || inst.SSHReady {
		t.Fatalf("new guest must start not-ready: %+v %v", inst.SSHReady, err)
	}

	// Release the old watcher's pending reply (it reports SSHReady=true).
	close(blockStatus)
	select {
	case <-watcherDone:
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not finish")
	}

	// The stale reply must NOT have flipped the new guest to ready.
	inst, err = f.svc.GetAnyInstance(f.inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	if inst.SSHReady {
		t.Fatal("stale watcher reply marked the reinstalled guest SSH-ready")
	}
}
