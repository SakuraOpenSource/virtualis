package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/SakuraOpenSource/virtualis/internal/model"
)

func TestSnapshotCreatePersistsAndLists(t *testing.T) {
	f := newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/snapshots") {
			var p struct {
				Instance struct {
					ID uint `json:"id"`
				} `json:"instance"`
				Name, Action string
			}
			if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
				t.Error(err)
			}
			if p.Name != "before-update" || p.Action != "create" || p.Instance.ID == 0 {
				t.Errorf("wrong snapshot payload: %+v", p)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"size_bytes": int64(1234)})
			return
		}
		replyLifecycle(w, decodeLifecycleRequest(t, r))
	})
	snap, err := f.svc.CreateSnapshot(context.Background(), f.inst.ID, RecoveryInput{Name: "before-update", Remark: "rollback point"})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != "available" || snap.SizeBytes != 1234 || snap.AgentID != f.agent.ID {
		t.Fatalf("snapshot: %+v", snap)
	}
	items, err := f.svc.ListSnapshots(f.inst.ID)
	if err != nil || len(items) != 1 || items[0].ID != snap.ID {
		t.Fatalf("list: %+v %v", items, err)
	}
	if _, err = f.svc.CreateSnapshot(context.Background(), f.inst.ID, RecoveryInput{Name: "before-update"}); err == nil {
		t.Fatal("duplicate accepted")
	}
	var inst model.Instance
	if err = f.db.First(&inst, f.inst.ID).Error; err != nil || inst.BusyOperation != "" {
		t.Fatalf("fence not released: %+v %v", inst, err)
	}
}

func TestSnapshotRestoreAndDeleteFailureSafety(t *testing.T) {
	running, failDelete := false, true
	f := newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/snapshots") {
			var p struct {
				Instance map[string]any `json:"instance"`
				Action   string         `json:"action"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			if p.Instance["root_password"] != nil {
				t.Error("restore must not reset password")
			}
			if p.Action == "delete" && failDelete {
				http.Error(w, `{"error":"disk busy"}`, 502)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"size_bytes": 0})
			return
		}
		inst := decodeLifecycleRequest(t, r)
		if running {
			inst.Status = "running"
		}
		replyLifecycle(w, inst)
	})
	f.inst.StoreSSHPassword("old-secret")
	if err := f.db.Model(&f.inst).Update("config_json", f.inst.ConfigJSON).Error; err != nil {
		t.Fatal(err)
	}
	snap, err := f.svc.CreateSnapshot(context.Background(), f.inst.ID, RecoveryInput{Name: "base"})
	if err != nil {
		t.Fatal(err)
	}
	running = true
	if _, err = f.svc.RestoreSnapshot(context.Background(), f.inst.ID, snap.ID); err == nil {
		t.Fatal("restored running runtime")
	}
	running = false
	if err = f.db.Model(&f.inst).Update("config_json", `{"ssh_password":"changed"}`).Error; err != nil {
		t.Fatal(err)
	}
	restored, err := f.svc.RestoreSnapshot(context.Background(), f.inst.ID, snap.ID)
	if err != nil || restored.LoadSSHPassword() != "old-secret" || restored.Network.IPv4 != f.inst.Network.IPv4 || restored.SSHReady {
		t.Fatalf("restored=%+v err=%v", restored, err)
	}
	if err = f.svc.DeleteSnapshot(context.Background(), f.inst.ID, snap.ID); err == nil {
		t.Fatal("delete failure swallowed")
	}
	items, err := f.svc.ListSnapshots(f.inst.ID)
	if err != nil || len(items) != 1 {
		t.Fatal("lost snapshot on delete failure")
	}
	failDelete = false
	if err = f.svc.DeleteSnapshot(context.Background(), f.inst.ID, snap.ID); err != nil {
		t.Fatal(err)
	}
	if items, err = f.svc.ListSnapshots(f.inst.ID); err != nil || len(items) != 0 {
		t.Fatalf("delete: %+v %v", items, err)
	}
}
