package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/SakuraOpenSource/virtualis/internal/agentclient"
	"github.com/SakuraOpenSource/virtualis/internal/model"
)

// VIR-CORE-01: a snapshot restore whose remote outcome is unknown must retain
// the durable fence, matching backup_restore semantics.
func TestSnapshotRestoreUnknownRemoteOutcomeRetainsFence(t *testing.T) {
	restored := false
	f := newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/snapshots") {
			var p struct {
				Action string `json:"action"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			if p.Action == "restore" {
				restored = true
				// The agent already replaced the disk but the response is lost.
				w.WriteHeader(http.StatusBadGateway)
				w.Write([]byte(`{"error":"connection reset mid-transfer"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"size_bytes": int64(1234)})
			return
		}
		replyLifecycle(w, decodeLifecycleRequest(t, r))
	})
	snap, err := f.svc.CreateSnapshot(context.Background(), f.inst.ID, RecoveryInput{Name: "base"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.RestoreSnapshot(context.Background(), f.inst.ID, snap.ID); err == nil {
		t.Fatal("uncertain restore reported success")
	}
	if !restored {
		t.Fatal("agent restore call missing")
	}
	inst, err := f.svc.GetAnyInstance(f.inst.ID)
	if err != nil || inst.BusyOperation == "" || inst.RecoveryError == "" {
		t.Fatalf("fence released after unknown restore outcome: %+v %v", inst, err)
	}
	// Follow-up power must stay rejected while the fence is retained.
	if _, err = f.svc.PowerInstance(context.Background(), f.inst.ID, "start", nil); err == nil {
		t.Fatal("power allowed while recovery unverified")
	}
}

// VIR-CORE-01 (contrast): preflight rejections that provably never reached the
// agent (running runtime) must NOT retain the fence.
func TestSnapshotRestorePreflightRejectDoesNotRetainFence(t *testing.T) {
	running := false
	f := newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/snapshots") {
			_ = json.NewEncoder(w).Encode(map[string]any{"size_bytes": int64(1234)})
			return
		}
		inst := decodeLifecycleRequest(t, r)
		if running {
			inst.Status = "running"
		}
		replyLifecycle(w, inst)
	})
	snap, err := f.svc.CreateSnapshot(context.Background(), f.inst.ID, RecoveryInput{Name: "base"})
	if err != nil {
		t.Fatal(err)
	}
	running = true
	if _, err = f.svc.RestoreSnapshot(context.Background(), f.inst.ID, snap.ID); err == nil {
		t.Fatal("restore of running instance accepted")
	}
	inst, err := f.svc.GetAnyInstance(f.inst.ID)
	if err != nil || inst.BusyOperation != "" {
		t.Fatalf("preflight rejection left durable fence: %+v %v", inst, err)
	}
}

// VIR-CORE-04: master-side resize preflight must enforce the same memory floor
// as the agent (128 MB) and reject with 400 without leaving a durable fence.
func TestResizeRejectsBelowAgentMemoryFloorWithoutFence(t *testing.T) {
	calls := 0
	f := newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/resize") {
			calls++
			var p struct {
				Instance agentclient.Instance `json:"instance"`
				Spec     model.InstanceSpec   `json:"spec"`
				Network  model.NetworkConfig  `json:"network"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			// Mirror the agent's preflight: below its memory floor reject
			// with 400 before touching anything; at/above it succeed and
			// echo back the applied resources.
			if p.Spec.MemoryMB < 128 {
				http.Error(w, `{"error":"invalid CPU/memory/disk specification"}`, http.StatusBadRequest)
				return
			}
			p.Instance.Spec = p.Spec
			p.Instance.Network = p.Network
			p.Instance.Status = "stopped"
			replyLifecycle(w, p.Instance)
			return
		}
		replyLifecycle(w, decodeLifecycleRequest(t, r))
	})
	_, err := f.svc.ResizeInstance(context.Background(), f.inst.ID, ResizeInput{Spec: model.InstanceSpec{CPU: 1, MemoryMB: 64, DiskGB: 20}})
	if err == nil {
		t.Fatal("64MB resize accepted")
	}
	var be *BizError
	if !errors.As(err, &be) || be.Status != http.StatusBadRequest {
		t.Fatalf("want 400, got %v", err)
	}
	inst, err := f.svc.GetAnyInstance(f.inst.ID)
	if err != nil || inst.BusyOperation != "" {
		t.Fatalf("invalid input left durable fence: %+v %v", inst, err)
	}
	if calls != 0 {
		t.Fatal("invalid resize reached the agent")
	}
	// A corrected request remains possible without manual recovery.
	if _, err = f.svc.ResizeInstance(context.Background(), f.inst.ID, ResizeInput{Spec: model.InstanceSpec{CPU: 1, MemoryMB: 128, DiskGB: 20}}); err != nil {
		t.Fatalf("valid retry after 400 rejected: %v", err)
	}
}

// VIR-CORE-04: truly ambiguous agent errors must still retain the fence.
func TestResizeAmbiguousAgentErrorRetainsFence(t *testing.T) {
	f := newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/resize") {
			http.Error(w, `{"error":"agent crashed mid-operation"}`, http.StatusBadGateway)
			return
		}
		replyLifecycle(w, decodeLifecycleRequest(t, r))
	})
	_, err := f.svc.ResizeInstance(context.Background(), f.inst.ID, ResizeInput{Spec: model.InstanceSpec{CPU: 1, MemoryMB: 2048, DiskGB: 20}})
	if err == nil {
		t.Fatal("agent failure reported success")
	}
	inst, gerr := f.svc.GetAnyInstance(f.inst.ID)
	if gerr != nil || inst.BusyOperation == "" {
		t.Fatalf("ambiguous resize released fence: %+v %v", inst, gerr)
	}
}

// VIR-CORE-05: success responses must not carry the already-released busy token.
func TestPowerSuccessResponseHasNoBusyToken(t *testing.T) {
	f := newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		inst := decodeLifecycleRequest(t, r)
		inst.Status = model.InstanceStatusStopped
		replyLifecycle(w, inst)
	})
	inst, err := f.svc.PowerInstance(context.Background(), f.inst.ID, "stop", nil)
	if err != nil {
		t.Fatal(err)
	}
	if inst.BusyOperation != "" || inst.BusyAction != "" || inst.BusySince != nil {
		t.Fatalf("success response carries released fence: %+v", inst.BusyOperation)
	}
	after, err := f.svc.GetAnyInstance(f.inst.ID)
	if err != nil || after.BusyOperation != "" {
		t.Fatalf("database fence not released: %+v %v", after, err)
	}
}

func TestTrashRestoreSuccessResponseHasNoBusyToken(t *testing.T) {
	f := newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		replyLifecycle(w, decodeLifecycleRequest(t, r))
	})
	trashAt := f.inst.CreatedAt
	if err := f.db.Model(&f.inst).Update("trashed_at", trashAt).Error; err != nil {
		t.Fatal(err)
	}
	inst, err := f.svc.RestoreTrashedInstance(context.Background(), f.inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	if inst.BusyOperation != "" {
		t.Fatalf("trash restore response carries released fence: %+v", inst.BusyOperation)
	}
}

func TestSnapshotRestoreSuccessResponseHasNoBusyToken(t *testing.T) {
	f := newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/snapshots") {
			_ = json.NewEncoder(w).Encode(map[string]any{"size_bytes": int64(1234)})
			return
		}
		replyLifecycle(w, decodeLifecycleRequest(t, r))
	})
	snap, err := f.svc.CreateSnapshot(context.Background(), f.inst.ID, RecoveryInput{Name: "base"})
	if err != nil {
		t.Fatal(err)
	}
	inst, err := f.svc.RestoreSnapshot(context.Background(), f.inst.ID, snap.ID)
	if err != nil {
		t.Fatal(err)
	}
	if inst.BusyOperation != "" {
		t.Fatalf("snapshot restore response carries released fence: %+v", inst.BusyOperation)
	}
}

// VIR-CORE-07: the background SSH readiness watcher must not touch the agent
// or persist readiness for an instance fenced by another operation.
func TestWatchSSHReadySkipsFencedInstance(t *testing.T) {
	called := false
	f := newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		inst := decodeLifecycleRequest(t, r)
		inst.SSHReady = true
		replyLifecycle(w, inst)
	})
	if err := f.db.Model(&f.inst).Updates(map[string]any{"busy_operation": "foreign-worker", "ssh_ready": false}).Error; err != nil {
		t.Fatal(err)
	}
	f.svc.watchSSHReady(f.inst.ID, &f.agent)
	if called {
		t.Fatal("watcher reached agent despite durable fence")
	}
	inst, err := f.svc.GetAnyInstance(f.inst.ID)
	if err != nil || inst.SSHReady {
		t.Fatalf("watcher persisted readiness on fenced instance: %+v %v", inst, err)
	}
}

// VIR-CORE-03: VPC deletion must be blocked while a fenced migration still
// references the source VPC.
func TestVPCDeleteBlockedByFencedMigrationSource(t *testing.T) {
	f := newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		replyLifecycle(w, decodeLifecycleRequest(t, r))
	})
	vpc := model.VPC{AgentID: f.agent.ID, Name: "src-net", Driver: model.DriverQEMU, Subnet: "10.10.0.0/24", Gateway: "10.10.0.1", State: "available"}
	if err := f.db.Create(&vpc).Error; err != nil {
		t.Fatal(err)
	}
	sourceVPC := vpc.ID
	if err := f.db.Model(&f.inst).Update("vpc_id", sourceVPC).Error; err != nil {
		t.Fatal(err)
	}
	mig := model.Migration{InstanceID: f.inst.ID, OperationID: "op-fenced", SourceAgentID: f.agent.ID, TargetAgentID: f.agent.ID + 1, TargetVPCID: nil, SourceVPCID: &sourceVPC, Stage: "source_delete"}
	if err := f.db.Create(&mig).Error; err != nil {
		t.Fatal(err)
	}
	// Remove the instance reference (as after a DB switch) — the migration's
	// source VPC reservation must still block deletion.
	if err := f.db.Model(&f.inst).Update("vpc_id", nil).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.svc.DeleteVPC(context.Background(), vpc.ID); err == nil {
		t.Fatal("source VPC of a fenced migration was deletable")
	}
	var stored model.VPC
	if err := f.db.First(&stored, vpc.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.State == "deleting" {
		t.Fatal("VPC row transitioned to deleting despite reservation")
	}
}

// VIR-CORE-09: agent failure messages must not leak absolute host paths.
func TestAgentFailureStripsAbsolutePaths(t *testing.T) {
	f := newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/import") {
			http.Error(w, `{"error":"rollback failed; archive retained at /var/lib/virtualis-agent/rollback-1234.tar"}`, http.StatusBadGateway)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/export") {
			w.Write([]byte("archive"))
			return
		}
		replyLifecycle(w, decodeLifecycleRequest(t, r))
	})
	backup, err := f.svc.CreateBackup(context.Background(), f.inst.ID, RecoveryInput{Name: "base"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.RestoreBackup(context.Background(), f.inst.ID, backup.ID)
	if err == nil {
		t.Fatal("restore failure swallowed")
	}
	if strings.Contains(err.Error(), "/var/lib/virtualis-agent") {
		t.Fatalf("absolute host path leaked to API callers: %v", err)
	}
	if !strings.Contains(err.Error(), "rollback-1234.tar") {
		t.Fatalf("basename diagnostic lost entirely: %v", err)
	}
}
