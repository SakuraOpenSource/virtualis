package server

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestSecurityGroupOwnershipWriteIsolationAndV1ReadOnlyScope(t *testing.T) {
	f := newAPIFixture(t)
	res := f.request(t, "POST", "/api/admin/security-groups", `{"name":"owned"}`, "", &f.admin)
	if res.Code != 200 {
		t.Fatalf("create: %d %s", res.Code, res.Body.String())
	}
	var group struct {
		ID uint `json:"id"`
	}
	json.Unmarshal(res.Body.Bytes(), &group)
	groupPath := fmt.Sprintf("/api/admin/security-groups/%d", group.ID)
	if res := f.request(t, "PUT", fmt.Sprintf("/api/instances/%d/security-groups", f.inst.ID), fmt.Sprintf(`{"security_group_ids":[%d]}`, group.ID), "", &f.owner); res.Code != 403 {
		t.Fatalf("non-admin bind: %d %s", res.Code, res.Body.String())
	}
	if res := f.request(t, "DELETE", groupPath, "", "", &f.other); res.Code != 403 {
		t.Fatalf("non-admin delete: %d", res.Code)
	}
	if res := f.request(t, "POST", "/api/v1/security-groups", `{"name":"scope"}`, f.readonly, nil); res.Code != 403 {
		t.Fatalf("readonly key write: %d %s", res.Code, res.Body.String())
	}
	if res := f.request(t, "GET", "/api/v1/security-groups", "", f.readonly, nil); res.Code != 200 {
		t.Fatalf("readonly key list: %d", res.Code)
	}
}
