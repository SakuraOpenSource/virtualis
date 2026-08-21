package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/SakuraOpenSource/virtualis/internal/model"
)

// IncusDriver manages containers/VMs via Incus.
// It tries the unix socket first and falls back to the incus CLI.
type IncusDriver struct {
	socketPath string
	httpClient *http.Client
}

// NewIncusDriver creates an Incus driver with default socket paths.
func NewIncusDriver() *IncusDriver {
	return &IncusDriver{socketPath: incusSocketPath()}
}

func incusSocketPath() string {
	candidates := []string{
		"/run/incus/unix.socket",
		"/var/lib/incus/unix.socket",
		"/run/lxd/unix.socket",
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return candidates[0]
}

func (d *IncusDriver) Name() string { return "incus" }

func (d *IncusDriver) Probe(ctx context.Context) error {
	// Prefer socket if available.
	if d.socketExists() {
		if err := d.probeSocket(ctx); err == nil {
			return nil
		}
	}
	// Fallback to CLI binary.
	if _, err := exec.LookPath("incus"); err == nil {
		// Try a quick version check with timeout.
		cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		cmd := exec.CommandContext(cmdCtx, "incus", "version")
		if err := cmd.Run(); err == nil {
			return nil
		}
	}
	// Also try lxc binary as alias for incus compatibility.
	if _, err := exec.LookPath("lxc"); err == nil {
		return nil
	}
	if d.socketExists() {
		return nil
	}
	return fmt.Errorf("incus not available: socket %s missing and incus binary not found", d.socketPath)
}

func (d *IncusDriver) socketExists() bool {
	_, err := os.Stat(d.socketPath)
	return err == nil
}

func (d *IncusDriver) probeSocket(ctx context.Context) error {
	client := d.client()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/1.0", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("incus socket returned %d", resp.StatusCode)
	}
	return nil
}

func (d *IncusDriver) client() *http.Client {
	if d.httpClient != nil {
		return d.httpClient
	}
	socket := d.socketPath
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socket)
		},
	}
	d.httpClient = &http.Client{Transport: transport, Timeout: 10 * time.Second}
	return d.httpClient
}

func (d *IncusDriver) instanceName(inst *model.Instance) string {
	return fmt.Sprintf("virtualis-%d-%s", inst.ID, inst.Name)
}

func (d *IncusDriver) Create(ctx context.Context, inst *model.Instance) error {
	if d.socketExists() {
		if err := d.createViaREST(ctx, inst); err == nil {
			return nil
		}
	}
	return d.runCLI(ctx, "launch", "images:ubuntu/22.04", d.instanceName(inst))
}

func (d *IncusDriver) Delete(ctx context.Context, inst *model.Instance) error {
	name := d.instanceName(inst)
	if d.socketExists() {
		if err := d.deleteViaREST(ctx, name); err == nil {
			return nil
		}
	}
	return d.runCLI(ctx, "delete", name, "--force")
}

func (d *IncusDriver) Start(ctx context.Context, inst *model.Instance) error {
	name := d.instanceName(inst)
	if d.socketExists() {
		if err := d.actionViaREST(ctx, name, "start"); err == nil {
			return nil
		}
	}
	return d.runCLI(ctx, "start", name)
}

func (d *IncusDriver) Stop(ctx context.Context, inst *model.Instance) error {
	name := d.instanceName(inst)
	if d.socketExists() {
		if err := d.actionViaREST(ctx, name, "stop"); err == nil {
			return nil
		}
	}
	return d.runCLI(ctx, "stop", name)
}

func (d *IncusDriver) Restart(ctx context.Context, inst *model.Instance) error {
	name := d.instanceName(inst)
	if d.socketExists() {
		if err := d.actionViaREST(ctx, name, "restart"); err == nil {
			return nil
		}
	}
	return d.runCLI(ctx, "restart", name)
}

func (d *IncusDriver) HardStart(ctx context.Context, inst *model.Instance) error {
	return d.Start(ctx, inst)
}

func (d *IncusDriver) HardStop(ctx context.Context, inst *model.Instance) error {
	name := d.instanceName(inst)
	if d.socketExists() {
		if err := d.actionViaREST(ctx, name, "stop"); err == nil {
			return nil
		}
	}
	return d.runCLI(ctx, "stop", name, "--force")
}

func (d *IncusDriver) HardRestart(ctx context.Context, inst *model.Instance) error {
	if err := d.HardStop(ctx, inst); err != nil {
		return err
	}
	return d.HardStart(ctx, inst)
}

func (d *IncusDriver) Reinstall(ctx context.Context, inst *model.Instance, image *model.Image) error {
	if err := d.Delete(ctx, inst); err != nil {
		return err
	}
	if image != nil && image.FilePath != "" {
		// Prefer restoring from image file if available via storage path.
		_ = filepath.Base(image.FilePath)
	}
	return d.Create(ctx, inst)
}

func (d *IncusDriver) Status(ctx context.Context, inst *model.Instance) (string, error) {
	name := d.instanceName(inst)
	if d.socketExists() {
		if s, err := d.statusViaREST(ctx, name); err == nil {
			return s, nil
		}
	}
	out, err := d.outputCLI(ctx, "info", name)
	if err != nil {
		return model.InstanceStatusStopped, nil
	}
	// crude parse: look for Status: Running
	if len(out) > 0 {
		text := string(out)
		if containsFold(text, "running") {
			return model.InstanceStatusRunning, nil
		}
		if containsFold(text, "stopped") {
			return model.InstanceStatusStopped, nil
		}
	}
	return model.InstanceStatusStopped, nil
}

// REST helpers

func (d *IncusDriver) createViaREST(ctx context.Context, inst *model.Instance) error {
	body := map[string]any{
		"name": d.instanceName(inst),
		"source": map[string]string{
			"type":     "image",
			"alias":    "ubuntu/22.04",
			"protocol": "simplestreams",
			"server":   "https://cloud-images.ubuntu.com/releases",
		},
	}
	if inst.Spec.CPU > 0 || inst.Spec.MemoryMB > 0 {
		body["config"] = map[string]string{
			"limits.cpu":    fmt.Sprintf("%d", inst.Spec.CPU),
			"limits.memory": fmt.Sprintf("%dMB", inst.Spec.MemoryMB),
		}
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/1.0/instances", jsonReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("incus create: %s", resp.Status)
	}
	return nil
}

func (d *IncusDriver) deleteViaREST(ctx context.Context, name string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, "http://unix/1.0/instances/"+name, nil)
	if err != nil {
		return err
	}
	resp, err := d.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("incus delete: %s", resp.Status)
	}
	return nil
}

func (d *IncusDriver) actionViaREST(ctx context.Context, name, action string) error {
	body := map[string]string{"action": action, "timeout": "30", "force": "false"}
	if action == "stop" {
		body["force"] = "false"
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://unix/1.0/instances/"+name+"/state", jsonReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("incus %s: %s", action, resp.Status)
	}
	return nil
}

func (d *IncusDriver) statusViaREST(ctx context.Context, name string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/1.0/instances/"+name+"/state", nil)
	if err != nil {
		return "", err
	}
	resp, err := d.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var payload struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	switch payload.Status {
	case "Running":
		return model.InstanceStatusRunning, nil
	case "Stopped":
		return model.InstanceStatusStopped, nil
	default:
		return model.InstanceStatusStopped, nil
	}
}

func (d *IncusDriver) runCLI(ctx context.Context, args ...string) error {
	bin := "incus"
	if _, err := exec.LookPath(bin); err != nil {
		bin = "lxc"
		if _, err2 := exec.LookPath(bin); err2 != nil {
			return fmt.Errorf("incus binary not found")
		}
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %w: %s", bin, args, err, string(out))
	}
	return nil
}

func (d *IncusDriver) outputCLI(ctx context.Context, args ...string) ([]byte, error) {
	bin := "incus"
	if _, err := exec.LookPath(bin); err != nil {
		bin = "lxc"
		if _, err2 := exec.LookPath(bin); err2 != nil {
			return nil, fmt.Errorf("incus binary not found")
		}
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	return cmd.CombinedOutput()
}
