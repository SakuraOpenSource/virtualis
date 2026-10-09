package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/SakuraOpenSource/virtualis/internal/agentclient"
	"github.com/SakuraOpenSource/virtualis/internal/model"
)

// fixImages creates two available images for reinstall-selection tests.
func fixImages(t *testing.T, f lifecycleFixture) (model.Image, model.Image) {
	t.Helper()
	a := model.Image{Name: "base-a", Driver: model.DriverQEMU, Type: "vm", Status: model.ImageStatusAvailable}
	b := model.Image{Name: "base-b", Driver: model.DriverQEMU, Type: "vm", Status: model.ImageStatusAvailable}
	if err := f.db.Create(&a).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&b).Error; err != nil {
		t.Fatal(err)
	}
	return a, b
}

// REV-REINSTALL-IMAGE: after selecting image B for a reinstall, the wire
// payload sent to the agent must reference B (both the instance ImageID and
// the embedded image metadata), and the persisted association must remain B.
// The old code preloaded Image=A in GetInstance, wrote image_id=B but kept
// instance.Image=A, so openImage/toWireImage sent A's metadata and the
// follow-up GORM Updates wrote A's association back over B.
func TestReinstallUsesSelectedImageInWireAndDatabase(t *testing.T) {
	var mu sync.Mutex
	var wireImageID *uint
	var wireMetaID *uint
	powerCalls := 0
	f := newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/power") {
			powerCalls++
			var p struct {
				Action   string               `json:"action"`
				Instance agentclient.Instance `json:"instance"`
			}
			if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
				t.Errorf("decode power payload: %v", err)
			}
			mu.Lock()
			wireImageID = p.Instance.ImageID
			if p.Instance.Image != nil {
				id := p.Instance.Image.ID
				wireMetaID = &id
			}
			mu.Unlock()
			p.Instance.Status = model.InstanceStatusStopped
			replyLifecycle(w, p.Instance)
			return
		}
		replyLifecycle(w, decodeLifecycleRequest(t, r))
	})
	imageA, imageB := fixImages(t, f)
	if err := f.db.Model(&f.inst).Update("image_id", imageA.ID).Error; err != nil {
		t.Fatal(err)
	}

	// Reinstall with image B selected; the request pointer is captured
	// BEFORE the call so a GORM mutation of it cannot fake a pass.
	expected := imageB.ID
	if _, err := f.svc.PowerInstance(context.Background(), f.inst.ID, model.ActionReinstall, &expected); err != nil {
		t.Fatalf("reinstall failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if powerCalls != 1 {
		t.Fatalf("expected exactly one power RPC, got %d", powerCalls)
	}
	if wireImageID == nil || *wireImageID != imageB.ID {
		t.Fatalf("wire instance image_id = %v, want %d", wireImageID, imageB.ID)
	}
	if wireMetaID == nil || *wireMetaID != imageB.ID {
		t.Fatalf("wire image metadata id = %v, want %d", wireMetaID, imageB.ID)
	}

	// The persisted association must be B, not restored to A by the GORM
	// association save-back.
	var stored model.Instance
	if err := f.db.First(&stored, f.inst.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.ImageID == nil || *stored.ImageID != imageB.ID {
		t.Fatalf("database image_id = %v, want %d", stored.ImageID, imageB.ID)
	}
}

// REV-REINSTALL-FENCE: a reinstall whose remote outcome is uncertain (agent
// reply lost mid-transfer) must retain the durable busy fence, mirroring
// snapshot_restore semantics. The reinstall already replaced the disk on the
// node; releasing the fence lets a follow-up start run an unknown disk state.
func TestReinstallUncertainOutcomeRetainsFence(t *testing.T) {
	f := newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/power") {
			// Remote applied the destructive reinstall, then the response
			// was lost.
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":"connection reset mid-transfer"}`))
			return
		}
		replyLifecycle(w, decodeLifecycleRequest(t, r))
	})
	a, b := fixImages(t, f)
	if err := f.db.Model(&f.inst).Update("image_id", a.ID).Error; err != nil {
		t.Fatal(err)
	}
	target := b.ID
	if _, err := f.svc.PowerInstance(context.Background(), f.inst.ID, model.ActionReinstall, &target); err == nil {
		t.Fatal("uncertain reinstall reported success")
	}
	inst, err := f.svc.GetAnyInstance(f.inst.ID)
	if err != nil || inst.BusyOperation == "" || inst.RecoveryError == "" {
		t.Fatalf("fence released after unknown reinstall outcome: busy=%q recovery=%q err=%v", inst.BusyOperation, inst.RecoveryError, err)
	}
	// Follow-up power must stay rejected while the fence is retained.
	if _, err = f.svc.PowerInstance(context.Background(), f.inst.ID, "start", nil); err == nil {
		t.Fatal("start allowed while recovery unverified")
	}
}
