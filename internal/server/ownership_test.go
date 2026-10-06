package server

import (
	"bytes"
	"fmt"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"testing"
)

func TestInstanceListDoesNotLeakOtherOwnersAndSessionCreateRequiresAdmin(t *testing.T) {
	f := newAPIFixture(t)
	other := model.Instance{Name: "other-private", OwnerID: &f.other.ID}
	f.db.Create(&other)
	res := f.request(t, "GET", "/api/instances", "", "", &f.owner)
	if res.Code != 200 || bytes.Contains(res.Body.Bytes(), []byte("other-private")) {
		t.Fatal("list leaks another tenant", res.Code, res.Body.String())
	}
	res = f.request(t, "POST", "/api/instances", `{"name":"arbitrary"}`, "", &f.owner)
	if res.Code != 403 {
		t.Fatalf("non-admin provision: %d %s", res.Code, res.Body.String())
	}
	for _, path := range []string{fmt.Sprintf("/api/trash/%d/restore", f.inst.ID), fmt.Sprintf("/api/instances/%d/purge", f.inst.ID)} {
		res = f.request(t, "POST", path, `{}`, "", &f.other)
		if res.Code != 403 && res.Code != 404 {
			t.Fatalf("ownership %s: %d", path, res.Code)
		}
	}
}
