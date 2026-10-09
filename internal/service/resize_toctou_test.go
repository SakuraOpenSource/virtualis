package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/SakuraOpenSource/virtualis/internal/agentclient"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"gorm.io/gorm"
)

// REV-RESIZE-TOCTOU: resize validation (disk shrink, architecture) must be
// re-run against the CURRENT instance row AFTER the fence is taken. The
// first read can see a stale spec when a previous resize completes between
// this request's read and its beginOperation; the old code then sent
// desired=20/current=30 to the agent, whose defensive 400 arrived after the
// fence was held and was treated as an uncertain outcome (durable fence,
// instance permanently busy). The callback seam injects exactly that
// interleaving: the service's pre-fence read still returns disk=20, while
// the row grows to 30 before the fence is claimed.
func TestResizeRevalidatesSpecInsideFence(t *testing.T) {
	resizeCalls := 0
	f := newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/resize") {
			resizeCalls++
			var p struct {
				Instance agentclient.Instance `json:"instance"`
				Spec     model.InstanceSpec   `json:"spec"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			// Mirror the agent's defensive rejection of a shrink.
			if p.Spec.DiskGB < p.Instance.Spec.DiskGB {
				http.Error(w, `{"error":"shrinking not supported"}`, http.StatusBadRequest)
				return
			}
			p.Instance.Spec = p.Spec
			p.Instance.Status = model.InstanceStatusStopped
			replyLifecycle(w, p.Instance)
			return
		}
		replyLifecycle(w, decodeLifecycleRequest(t, r))
	})

	// GORM after-query callback as the interleaving seam: when the service
	// reads THIS instance row for pre-fence validation (dest is a single
	// *model.Instance still showing disk=20), the competing operation
	// commits its growth to 30 before the fence can be claimed.
	grew := false
	if err := f.db.Callback().Query().After("gorm:query").Register("test:inject_competing_resize", func(tx *gorm.DB) {
		if grew || tx.Statement == nil || tx.Statement.Table != "instances" || tx.Statement.Dest == nil {
			return
		}
		inst, ok := tx.Statement.Dest.(*model.Instance)
		if !ok || inst == nil || inst.ID != f.inst.ID || inst.Spec.DiskGB != 20 || inst.BusyOperation != "" {
			return
		}
		grew = true
		specJSON, _ := json.Marshal(model.InstanceSpec{CPU: 1, MemoryMB: 2048, DiskGB: 30, Arch: "x86_64"})
		if e := f.db.Session(&gorm.Session{NewDB: true}).Exec("UPDATE instances SET spec = ? WHERE id = ?", string(specJSON), f.inst.ID).Error; e != nil {
			t.Errorf("inject competing resize: %v", e)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Callback().Query().Remove("test:inject_competing_resize") })

	// Request computed against the stale 20-row: disk=20 passes the
	// fence-EXTERNAL validation (fast 400s for caller errors), and must be
	// re-validated against the CURRENT row (disk=30) inside the fence.
	_, err := f.svc.ResizeInstance(context.Background(), f.inst.ID, ResizeInput{Spec: model.InstanceSpec{CPU: 1, MemoryMB: 2048, DiskGB: 20, Arch: "x86_64"}})
	if err == nil {
		t.Fatal("stale shrink request accepted")
	}
	if be, ok := AsError(err); !ok || be.Status != http.StatusBadRequest {
		t.Fatalf("want a plain 400 from fence-internal revalidation, got %v", err)
	}
	if resizeCalls != 0 {
		t.Fatalf("stale-spec resize reached the agent: %d calls", resizeCalls)
	}
	inst, gerr := f.svc.GetAnyInstance(f.inst.ID)
	if gerr != nil || inst.BusyOperation != "" {
		t.Fatalf("fence-internal rejection must release the fence: busy=%q err=%v", inst.BusyOperation, gerr)
	}
	// The competing growth must be intact.
	if inst.Spec.DiskGB != 30 {
		t.Fatalf("competing resize lost: disk=%d", inst.Spec.DiskGB)
	}
}
