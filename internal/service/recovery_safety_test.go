package service

import (
	"context"
	"encoding/json"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"net/http"
	"strings"
	"testing"
)

func TestDHCPObservedIPPersistsWithoutBecomingDesiredStaticAddress(t *testing.T) {
	f := newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		inst := decodeLifecycleRequest(t, r)
		raw, _ := json.Marshal(inst)
		var fields map[string]any
		json.Unmarshal(raw, &fields)
		fields["observed_ip"] = "10.100.0.9"
		json.NewEncoder(w).Encode(map[string]any{"instance": fields})
	})
	raw, _ := json.Marshal(model.NetworkConfig{Mode: "vpc", MAC: "52:54:00:00:00:02"})
	f.db.Model(&f.inst).Update("network", string(raw))
	inst, err := f.svc.RefreshStatus(context.Background(), f.inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	if inst.ObservedIP != "10.100.0.9" || inst.IP != "10.100.0.9" || inst.Network.IPv4 != "" {
		t.Fatalf("DHCP not synchronized or made static: %+v", inst)
	}
	payload, _ := json.Marshal(toWireInstance(inst, nil))
	if !strings.Contains(string(payload), `"observed_ip":"10.100.0.9"`) {
		t.Fatal("observation not forwarded to Agent firewall")
	}
}

func TestUncertainBackupRestoreRetainsFenceAndArchive(t *testing.T) {
	f := newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/export") {
			w.Write([]byte("archive"))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/import") {
			http.Error(w, "rollback failed; original archive retained", 502)
			return
		}
		replyLifecycle(w, decodeLifecycleRequest(t, r))
	})
	backup, err := f.svc.CreateBackup(context.Background(), f.inst.ID, RecoveryInput{Name: "base"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.RestoreBackup(context.Background(), f.inst.ID, backup.ID); err == nil {
		t.Fatal("failed restore succeeded")
	}
	inst, err := f.svc.GetInstance(f.inst.ID)
	if err != nil || inst.BusyOperation == "" || inst.RecoveryError == "" {
		t.Fatalf("uncertain restore lost fence: %+v %v", inst, err)
	}
	file, err := f.svc.storage.Open(backup.FilePath)
	if err != nil {
		t.Fatal("recovery archive lost", err)
	}
	file.Close()
}
