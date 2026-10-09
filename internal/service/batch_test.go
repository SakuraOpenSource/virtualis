package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/SakuraOpenSource/virtualis/internal/model"
)

func TestBatchPerEntryDedupesAndEnforcesOwnership(t *testing.T) {
	calls := 0
	f := newLifecycleFixture(t, func(w http.ResponseWriter, r *http.Request) { calls++; replyLifecycle(w, decodeLifecycleRequest(t, r)) })
	owner := uint(42)
	other := uint(43)
	if err := f.db.Model(&f.inst).Update("owner_id", owner).Error; err != nil {
		t.Fatal(err)
	}
	forbidden := f.inst
	forbidden.Base = model.Base{}
	forbidden.Name = "other"
	forbidden.OwnerID = &other
	if err := f.db.Create(&forbidden).Error; err != nil {
		t.Fatal(err)
	}
	result, err := f.svc.BatchInstances(context.Background(), BatchInput{IDs: []uint{f.inst.ID, 999, f.inst.ID, forbidden.ID}, Action: "stop"}, owner)
	if err != nil || len(result.OK) != 1 || result.OK[0] != f.inst.ID || len(result.Failed) != 2 || calls != 1 {
		t.Fatalf("batch=%+v err=%v calls=%d", result, err, calls)
	}
	if _, err = f.svc.BatchInstances(context.Background(), BatchInput{IDs: make([]uint, 101), Action: "stop"}, 0); err == nil {
		t.Fatal("oversized batch accepted")
	}
	if _, err = f.svc.BatchInstances(context.Background(), BatchInput{IDs: []uint{f.inst.ID}, Action: "purge"}, 0); err == nil {
		t.Fatal("unsupported batch action accepted")
	}
}
