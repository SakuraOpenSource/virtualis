package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestBackupStreamPersistsChecksumAndDownload(t *testing.T) {
	body := strings.Repeat("archive-bytes", 1024)
	f := newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		inst := decodeLifecycleRequest(t, r)
		if strings.HasSuffix(r.URL.Path, "/export") {
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			_, _ = io.WriteString(w, body)
			return
		}
		replyLifecycle(w, inst)
	})
	backup, err := f.svc.CreateBackup(context.Background(), f.inst.ID, RecoveryInput{Name: "full"})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(body))
	if backup.Checksum != hex.EncodeToString(digest[:]) || backup.SizeBytes != int64(len(body)) || backup.Format != "tar" || backup.Status != "available" {
		t.Fatalf("backup=%+v", backup)
	}
	if !strings.HasSuffix(backup.FilePath, ".tar") {
		t.Fatalf("archive extension missing: %s", backup.FilePath)
	}
	items, err := f.svc.ListBackups(f.inst.ID)
	if err != nil || len(items) != 1 {
		t.Fatalf("list: %+v %v", items, err)
	}
	file, meta, err := f.svc.OpenBackup(context.Background(), f.inst.ID, backup.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(file)
	_ = file.Close()
	if err != nil || string(got) != body || meta.ID != backup.ID {
		t.Fatal("download differs from exported archive")
	}
	if _, _, err = f.svc.OpenBackup(context.Background(), f.inst.ID+1, backup.ID); err == nil {
		t.Fatal("cross-instance backup accessible")
	}
}
