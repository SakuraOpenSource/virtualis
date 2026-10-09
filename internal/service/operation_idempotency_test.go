package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SakuraOpenSource/virtualis/internal/agentclient"
	"github.com/SakuraOpenSource/virtualis/internal/model"
)

// REV-OP-ID-RECOVERY: retrying a destructive reinstall with the SAME
// X-Levis-Operation-ID must not re-send the RPC after the first request
// already applied it. The id is a persistent idempotency key, not a log
// suffix.
func TestSameOperationIDDoesNotRepeatReinstall(t *testing.T) {
	var powerCalls atomic.Int32
	var wireImages []uint
	f := newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/power") {
			powerCalls.Add(1)
			var p struct {
				Action   string               `json:"action"`
				Instance agentclient.Instance `json:"instance"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			if p.Instance.Image != nil {
				wireImages = append(wireImages, p.Instance.Image.ID)
			}
			p.Instance.Status = model.InstanceStatusStopped
			replyLifecycle(w, p.Instance)
			return
		}
		replyLifecycle(w, decodeLifecycleRequest(t, r))
	})
	a, b := fixImages(t, f)
	if err := f.db.Model(&f.inst).Update("image_id", a.ID).Error; err != nil {
		t.Fatal(err)
	}
	const opID = "levis-reinstall-order-42"
	target := b.ID

	// First request applies the reinstall under opID.
	if _, err := f.svc.PowerInstance(context.Background(), f.inst.ID, model.ActionReinstall, &target, opID); err != nil {
		t.Fatalf("first reinstall: %v", err)
	}
	if got := powerCalls.Load(); got != 1 {
		t.Fatalf("first call: power RPC count = %d, want 1", got)
	}

	// Upstream retry with the SAME id: the operation already completed, so
	// the master must NOT re-send the destructive reinstall RPC. It returns
	// the recorded result.
	if _, err := f.svc.PowerInstance(context.Background(), f.inst.ID, model.ActionReinstall, &target, opID); err != nil {
		t.Fatalf("idempotent retry errored: %v", err)
	}
	if got := powerCalls.Load(); got != 1 {
		t.Fatalf("retry re-sent destructive reinstall: power RPC count = %d, want 1", got)
	}
	var stored model.Instance
	if err := f.db.First(&stored, f.inst.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.ImageID == nil || *stored.ImageID != b.ID {
		t.Fatalf("database image after retry = %v, want %d", stored.ImageID, b.ID)
	}
}

// REV-OP-ID-RECOVERY: the same id reused for a DIFFERENT action is a
// protocol violation and must be rejected with 409, not silently executed.
func TestSameOperationIDDifferentActionConflicts(t *testing.T) {
	f := newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		replyLifecycle(w, decodeLifecycleRequest(t, r))
	})
	const opID = "levis-op-dual-use"
	if _, err := f.svc.PowerInstance(context.Background(), f.inst.ID, "stop", nil, opID); err != nil {
		t.Fatalf("first op: %v", err)
	}
	_, err := f.svc.PowerInstance(context.Background(), f.inst.ID, "start", nil, opID)
	if err == nil {
		t.Fatal("same operation id accepted for a different action")
	}
	if be, ok := AsError(err); !ok || be.Status != http.StatusConflict {
		t.Fatalf("want 409, got %v", err)
	}
}

// REV-OP-ID-COLUMN-LENGTH: a legal 64-byte caller id must never overflow the
// varchar(64) operation_id column when composed with the internal token.
// The schema keeps the internal token and the caller id in SEPARATE
// columns; the caller column accepts up to 128 bytes (the plugin's
// validOperationID ceiling), and no composed value is written anywhere.
func TestOperationLogIDFitsSchemaWithLongCallerRef(t *testing.T) {
	f := newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		replyLifecycle(w, decodeLifecycleRequest(t, r))
	})
	longRef := strings.Repeat("x", 64)
	if _, err := f.svc.PowerInstance(context.Background(), f.inst.ID, "stop", nil, longRef); err != nil {
		t.Fatalf("power with 64-byte caller ref: %v", err)
	}
	var rows []model.InstanceOperationLog
	if err := f.db.Where("instance_id = ?", f.inst.ID).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("no operation logs persisted")
	}
	for _, row := range rows {
		if len(row.OperationID) > 64 {
			t.Fatalf("operation_id %q exceeds varchar(64): %d bytes", row.OperationID, len(row.OperationID))
		}
	}
	// The caller ref must survive intact (no truncation to 64) in its own
	// column for upstream correlation.
	var ops []model.InstanceOperation
	if err := f.db.Where("instance_id = ? AND caller_operation_id = ?", f.inst.ID, longRef).Find(&ops).Error; err != nil {
		t.Fatal(err)
	}
	if len(ops) == 0 {
		t.Fatal("128-capable caller_operation_id column missing or truncated the 64-byte ref")
	}
}
