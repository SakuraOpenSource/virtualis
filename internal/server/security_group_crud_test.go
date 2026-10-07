package server

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestSecurityGroupProviderCRUDRulesAndValidation(t *testing.T) {
	f := newAPIFixture(t)
	res := f.request(t, "POST", "/api/v1/security-groups", `{"name":"provider","ingress_policy":"accept"}`, f.key, nil)
	if res.Code != 200 {
		t.Fatalf("provider create: %d %s", res.Code, res.Body.String())
	}
	var group struct {
		ID uint `json:"id"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &group); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/security-groups/%d", group.ID)
	res = f.request(t, "PUT", path+"/rules", `{"rules":[{"direction":"in","action":"accept","protocol":"tcp","port_start":443,"priority":4}]}`, f.key, nil)
	if res.Code != 200 {
		t.Fatalf("replace rules: %d %s", res.Code, res.Body.String())
	}
	res = f.request(t, "PUT", path+"/rules", `{"rules":[{"direction":"in","action":"accept","protocol":"tcp","port_start":99999}]}`, f.key, nil)
	if res.Code != 400 {
		t.Fatalf("invalid port: %d %s", res.Code, res.Body.String())
	}
	res = f.request(t, "PATCH", path, `{"description":"patched","egress_policy":"drop"}`, f.key, nil)
	if res.Code != 200 {
		t.Fatalf("partial patch: %d %s", res.Code, res.Body.String())
	}
	res = f.request(t, "GET", path, "", f.key, nil)
	var got struct {
		Name        string           `json:"name"`
		Description string           `json:"description"`
		Egress      string           `json:"egress_policy"`
		Rules       []map[string]any `json:"rules"`
	}
	json.Unmarshal(res.Body.Bytes(), &got)
	if res.Code != 200 || got.Name != "provider" || got.Description != "patched" || got.Egress != "drop" || len(got.Rules) != 1 || got.Rules[0]["port_start"] != float64(443) {
		t.Fatalf("patch/rollback: %d %s", res.Code, res.Body.String())
	}
	res = f.request(t, "DELETE", path, "", f.key, nil)
	if res.Code != 204 {
		t.Fatalf("delete: %d %s", res.Code, res.Body.String())
	}
	res = f.request(t, "GET", path, "", f.key, nil)
	if res.Code != 404 {
		t.Fatalf("deleted group exists: %d", res.Code)
	}
}
