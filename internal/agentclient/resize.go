package agentclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"net/http"
)

func (c *Client) Resize(ctx context.Context, instance Instance, spec model.InstanceSpec, network model.NetworkConfig) (Instance, error) {
	var out struct {
		Instance Instance `json:"instance"`
	}
	err := c.resizeJSON(ctx, fmt.Sprintf("instances/%d/resize", instance.ID), map[string]any{"instance": instance, "spec": spec, "network": network}, &out)
	if err != nil {
		return Instance{}, err
	}
	if out.Instance.ID != instance.ID || out.Instance.Driver != instance.Driver || out.Instance.Status != "stopped" {
		return Instance{}, fmt.Errorf("resize returned invalid identity or state")
	}
	return out.Instance, nil
}
func (c *Client) resizeJSON(ctx context.Context, path string, payload any, out any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := c.newRequest(ctx, http.MethodPost, path, bytes.NewReader(raw), "application/json")
	if err != nil {
		return err
	}
	return c.recoveryClient().do(req, out)
}
