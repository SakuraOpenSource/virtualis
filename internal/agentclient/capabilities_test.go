package agentclient

import (
	"context"
	"fmt"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestProtectedLifecycleRejectsUnsupportedPolicyBeforeMutation(t *testing.T) {
	var mutations atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/drivers" {
			fmt.Fprint(w, `{"items":[{"name":"qemu","available":true,"firewall_policy":false}]}`)
			return
		}
		mutations.Add(1)
		fmt.Fprint(w, `{"instance":{"id":1,"driver":"qemu","status":"stopped"}}`)
	}))
	defer server.Close()
	client, err := New(server.URL, "fixture-token")
	if err != nil {
		t.Fatal(err)
	}
	inst := Instance{ID: 1, Driver: "qemu", FirewallPolicy: &model.FirewallPolicy{Ingress: "drop", Egress: "accept"}}
	ctx := context.Background()
	cases := map[string]func() error{
		"create":    func() error { _, err := client.CreateInstance(ctx, inst, nil, nil, "", nil, ""); return err },
		"start":     func() error { _, err := client.PowerInstance(ctx, inst, "start", nil, nil, "", nil, ""); return err },
		"restart":   func() error { _, err := client.PowerInstance(ctx, inst, "restart", nil, nil, "", nil, ""); return err },
		"configure": func() error { _, _, err := client.ConfigureNetwork(ctx, inst, model.NetworkConfig{}, ""); return err },
		"import": func() error {
			_, err := client.Import(ctx, inst, strings.NewReader("archive"), "fixture.tar", true)
			return err
		},
		"resize": func() error {
			_, err := client.Resize(ctx, inst, model.InstanceSpec{}, model.NetworkConfig{})
			return err
		},
		"status":   func() error { _, err := client.Status(ctx, inst); return err },
		"firewall": func() error { return client.ApplyFirewall(ctx, inst) },
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			if err := call(); err == nil {
				t.Error("unsupported policy reported success")
			}
		})
	}
	if mutations.Load() != 0 {
		t.Fatalf("unsupported policy reached %d lifecycle calls", mutations.Load())
	}
	// 即使能力不可用，关机/永久删除仍必须能够清理实例与保留地址。
	if _, err := client.PowerInstance(ctx, inst, "stop", nil, nil, "", nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}
}
