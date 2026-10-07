package agentclient

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// Import streams multipart from a file; closing the pipe on all exits releases
// the producer even if the Agent rejects headers without consuming the body.
func (c *Client) Import(ctx context.Context, instance Instance, file io.Reader, filename string, replace bool) (Instance, error) {
	if err := c.RequireFirewallPolicy(ctx, instance); err != nil {
		return Instance{}, err
	}
	body, contentType, err := multipartBody("instance", instance, "image", file, filename)
	if err != nil {
		return Instance{}, err
	}
	if closer, ok := body.(io.Closer); ok {
		defer closer.Close()
	}
	path := fmt.Sprintf("instances/%d/import", instance.ID)
	if replace {
		path += "?replace=true"
	}
	req, err := c.newRequest(ctx, http.MethodPost, path, body, contentType)
	if err != nil {
		return Instance{}, err
	}
	var out struct {
		Instance Instance `json:"instance"`
	}
	if err = c.recoveryClient().do(req, &out); err != nil {
		return Instance{}, err
	}
	if out.Instance.ID != instance.ID || out.Instance.Driver != instance.Driver || out.Instance.Status != "stopped" {
		return Instance{}, fmt.Errorf("import returned invalid instance identity or state")
	}
	return out.Instance, nil
}
