package agentclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const RecoveryTimeout = 2 * time.Hour

// A separate client keeps short control calls bounded at 90 seconds without
// timing out a multi-GiB transfer. It retains the no-redirect credential policy.
func (c *Client) recoveryClient() *Client {
	clone := *c
	hc := *c.httpClient
	hc.Timeout = RecoveryTimeout
	clone.httpClient = &hc
	return &clone
}
func (c *Client) Snapshot(ctx context.Context, instance Instance, name, action string) (int64, error) {
	raw, err := json.Marshal(map[string]any{"instance": instance, "name": name, "action": action})
	if err != nil {
		return 0, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, fmt.Sprintf("instances/%d/snapshots", instance.ID), bytes.NewReader(raw), "application/json")
	if err != nil {
		return 0, err
	}
	var out struct {
		SizeBytes int64 `json:"size_bytes"`
	}
	if err = c.recoveryClient().do(req, &out); err != nil {
		return 0, err
	}
	if out.SizeBytes < 0 {
		return 0, fmt.Errorf("invalid snapshot size")
	}
	return out.SizeBytes, nil
}

// Export returns a stream owned by the caller and the declared length (-1 when
// chunked). Error responses and oversized payloads are closed here.
func (c *Client) Export(ctx context.Context, instance Instance) (io.ReadCloser, int64, error) {
	raw, err := json.Marshal(map[string]any{"instance": instance})
	if err != nil {
		return nil, 0, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, fmt.Sprintf("instances/%d/export", instance.ID), bytes.NewReader(raw), "application/json")
	if err != nil {
		return nil, 0, err
	}
	resp, err := c.recoveryClient().httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, 0, fmt.Errorf("export failed (%d): %s", resp.StatusCode, raw)
	}
	if resp.ContentLength > 64<<30 {
		_ = resp.Body.Close()
		return nil, 0, fmt.Errorf("archive exceeds 64 GiB limit")
	}
	return resp.Body, resp.ContentLength, nil
}
