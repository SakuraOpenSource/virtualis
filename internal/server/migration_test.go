package server

import (
	"fmt"
	"testing"
)

func TestMigrationResizeRoutesAreAdminOrScoped(t *testing.T) {
	f := newAPIFixture(t)
	for _, endpoint := range []struct{ method, path, body string }{
		{"PATCH", "spec", `{"spec":{"cpu":2,"memory_mb":2048,"disk_gb":30}}`},
		{"POST", "migrate", `{"target_agent_id":2}`},
	} {
		path := fmt.Sprintf("/api/instances/%d/%s", f.inst.ID, endpoint.path)
		res := f.request(t, endpoint.method, path, endpoint.body, "", &f.owner)
		if res.Code != 403 {
			t.Fatalf("session %s: %d %s", endpoint.path, res.Code, res.Body.String())
		}
		path = fmt.Sprintf("/api/v1/instances/%d/%s", f.inst.ID, endpoint.path)
		res = f.request(t, endpoint.method, path, endpoint.body, f.readonly, nil)
		if res.Code != 403 {
			t.Fatalf("scope %s: %d", endpoint.path, res.Code)
		}
		res = f.request(t, endpoint.method, path, endpoint.body, f.key, nil)
		if res.Code == 404 {
			t.Fatalf("route missing %s", endpoint.path)
		}
	}
}
