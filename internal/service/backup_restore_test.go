package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/SakuraOpenSource/virtualis/internal/agentclient"
	"github.com/SakuraOpenSource/virtualis/internal/model"
)

func TestBackupRestoreChecksIntegrityAndPreservesNetwork(t *testing.T) {
	imports := 0
	f := newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/import") {
			imports++
			if r.URL.Query().Get("replace") != "true" {
				t.Error("missing replace")
			}
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
				return
			}
			defer r.MultipartForm.RemoveAll()
			var inst agentclient.Instance
			_ = json.Unmarshal([]byte(r.FormValue("instance")), &inst)
			file, _, err := r.FormFile("image")
			if err != nil {
				t.Error(err)
				return
			}
			data, _ := io.ReadAll(file)
			_ = file.Close()
			if string(data) != "good-archive" || inst.RootPassword != "" {
				t.Error("wrong recovery upload")
			}
			inst.Status = "stopped"
			replyLifecycle(w, inst)
			return
		}
		inst := decodeLifecycleRequest(t, r)
		if strings.HasSuffix(r.URL.Path, "/export") {
			_, _ = io.WriteString(w, "good-archive")
			return
		}
		replyLifecycle(w, inst)
	})
	backup, err := f.svc.CreateBackup(context.Background(), f.inst.ID, RecoveryInput{Name: "base"})
	if err != nil {
		t.Fatal(err)
	}
	path, err := f.svc.storage.Path(backup.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.RestoreBackup(context.Background(), f.inst.ID, backup.ID); err == nil {
		t.Fatal("corrupt archive restored")
	}
	if imports != 0 {
		t.Fatal("destructive import before validation")
	}
	if err = os.WriteFile(path, []byte("good-archive"), 0600); err != nil {
		t.Fatal(err)
	}
	inst, err := f.svc.RestoreBackup(context.Background(), f.inst.ID, backup.ID)
	if err != nil || inst.Network.IPv4 != f.inst.Network.IPv4 || inst.SSHReady || imports != 1 {
		t.Fatalf("restore: %+v %v calls=%d", inst, err, imports)
	}
	if err = f.svc.DeleteBackup(context.Background(), f.inst.ID, backup.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("backup file retained after successful deletion")
	}
	var n int64
	f.db.Model(&model.Backup{}).Count(&n)
	if n != 0 {
		t.Fatal("backup row retained")
	}
}
