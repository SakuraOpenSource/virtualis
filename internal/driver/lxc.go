package driver

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/SakuraOpenSource/virtualis/internal/model"
)

// LxcDriver manages containers via lxc-* CLI tools.
type LxcDriver struct{}

// NewLxcDriver creates a new LXC driver.
func NewLxcDriver() *LxcDriver { return &LxcDriver{} }

func (d *LxcDriver) Name() string { return "lxc" }

func (d *LxcDriver) Probe(ctx context.Context) error {
	// Check for at least lxc-info or lxc-ls.
	for _, bin := range []string{"lxc-info", "lxc-ls", "lxc-create"} {
		if _, err := exec.LookPath(bin); err == nil {
			// Optional: try running version with timeout
			cmd := exec.CommandContext(ctx, bin, "--version")
			_ = cmd.Run()
			return nil
		}
	}
	if _, err := exec.LookPath("lxc"); err == nil {
		return nil
	}
	return fmt.Errorf("lxc tools not found (looked for lxc-info, lxc-ls, lxc)")
}

func (d *LxcDriver) containerName(inst *model.Instance) string {
	return fmt.Sprintf("virtualis-%d-%s", inst.ID, inst.Name)
}

func (d *LxcDriver) Create(ctx context.Context, inst *model.Instance) error {
	name := d.containerName(inst)
	template := "ubuntu"
	// Try lxc-create
	if _, err := exec.LookPath("lxc-create"); err == nil {
		cmd := exec.CommandContext(ctx, "lxc-create", "-n", name, "-t", template)
		if out, err := cmd.CombinedOutput(); err == nil {
			return nil
		} else {
			return fmt.Errorf("lxc-create: %w: %s", err, string(out))
		}
	}
	// Fallback to lxc CLI
	return d.run(ctx, "create", name, template)
}

func (d *LxcDriver) Delete(ctx context.Context, inst *model.Instance) error {
	name := d.containerName(inst)
	if _, err := exec.LookPath("lxc-destroy"); err == nil {
		cmd := exec.CommandContext(ctx, "lxc-destroy", "-n", name, "-f")
		if out, err := cmd.CombinedOutput(); err == nil {
			return nil
		} else {
			// If container doesn't exist, treat as success.
			if strings.Contains(string(out), "does not exist") {
				return nil
			}
			return fmt.Errorf("lxc-destroy: %w: %s", err, string(out))
		}
	}
	return d.run(ctx, "delete", name, "--force")
}

func (d *LxcDriver) Start(ctx context.Context, inst *model.Instance) error {
	name := d.containerName(inst)
	if _, err := exec.LookPath("lxc-start"); err == nil {
		cmd := exec.CommandContext(ctx, "lxc-start", "-n", name, "-d")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("lxc-start: %w: %s", err, string(out))
		}
		return nil
	}
	return d.run(ctx, "start", name)
}

func (d *LxcDriver) Stop(ctx context.Context, inst *model.Instance) error {
	name := d.containerName(inst)
	if _, err := exec.LookPath("lxc-stop"); err == nil {
		cmd := exec.CommandContext(ctx, "lxc-stop", "-n", name)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("lxc-stop: %w: %s", err, string(out))
		}
		return nil
	}
	return d.run(ctx, "stop", name)
}

func (d *LxcDriver) Restart(ctx context.Context, inst *model.Instance) error {
	if err := d.Stop(ctx, inst); err != nil {
		return err
	}
	return d.Start(ctx, inst)
}

func (d *LxcDriver) HardStart(ctx context.Context, inst *model.Instance) error {
	return d.Start(ctx, inst)
}

func (d *LxcDriver) HardStop(ctx context.Context, inst *model.Instance) error {
	name := d.containerName(inst)
	if _, err := exec.LookPath("lxc-stop"); err == nil {
		cmd := exec.CommandContext(ctx, "lxc-stop", "-n", name, "-k")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("lxc-stop -k: %w: %s", err, string(out))
		}
		return nil
	}
	return d.run(ctx, "stop", name, "--force")
}

func (d *LxcDriver) HardRestart(ctx context.Context, inst *model.Instance) error {
	if err := d.HardStop(ctx, inst); err != nil {
		return err
	}
	return d.HardStart(ctx, inst)
}

func (d *LxcDriver) Reinstall(ctx context.Context, inst *model.Instance, _ *model.Image) error {
	if err := d.Delete(ctx, inst); err != nil {
		return err
	}
	return d.Create(ctx, inst)
}

func (d *LxcDriver) Status(ctx context.Context, inst *model.Instance) (string, error) {
	name := d.containerName(inst)
	if _, err := exec.LookPath("lxc-info"); err == nil {
		cmd := exec.CommandContext(ctx, "lxc-info", "-n", name)
		out, err := cmd.CombinedOutput()
		if err != nil {
			// Non-zero often means stopped or not found.
			text := string(out)
			if strings.Contains(text, "STOPPED") {
				return model.InstanceStatusStopped, nil
			}
			if strings.Contains(text, "does not exist") {
				return model.InstanceStatusStopped, nil
			}
			return model.InstanceStatusStopped, nil
		}
		text := string(out)
		if strings.Contains(text, "RUNNING") {
			return model.InstanceStatusRunning, nil
		}
		if strings.Contains(text, "STOPPED") {
			return model.InstanceStatusStopped, nil
		}
		if containsFold(text, "running") {
			return model.InstanceStatusRunning, nil
		}
		return model.InstanceStatusStopped, nil
	}
	// Fallback lxc list
	out, err := d.output(ctx, "list", name, "--format", "csv")
	if err != nil {
		return model.InstanceStatusStopped, nil
	}
	if containsFold(string(out), "running") {
		return model.InstanceStatusRunning, nil
	}
	return model.InstanceStatusStopped, nil
}

func (d *LxcDriver) run(ctx context.Context, args ...string) error {
	bin := "lxc"
	if _, err := exec.LookPath(bin); err != nil {
		return fmt.Errorf("lxc binary not found")
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("lxc %v: %w: %s", args, err, string(out))
	}
	return nil
}

func (d *LxcDriver) output(ctx context.Context, args ...string) ([]byte, error) {
	bin := "lxc"
	if _, err := exec.LookPath(bin); err != nil {
		return nil, fmt.Errorf("lxc binary not found")
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	return cmd.CombinedOutput()
}
