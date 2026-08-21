package driver

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/SakuraOpenSource/virtualis/internal/model"
)

// Driver abstracts a virtualization backend.
//
// Each backend must be stateless and safe for concurrent use.
type Driver interface {
	Name() string
	Probe(ctx context.Context) error
	Create(ctx context.Context, inst *model.Instance) error
	Delete(ctx context.Context, inst *model.Instance) error
	Start(ctx context.Context, inst *model.Instance) error
	Stop(ctx context.Context, inst *model.Instance) error
	Restart(ctx context.Context, inst *model.Instance) error
	HardStart(ctx context.Context, inst *model.Instance) error
	HardStop(ctx context.Context, inst *model.Instance) error
	HardRestart(ctx context.Context, inst *model.Instance) error
	Reinstall(ctx context.Context, inst *model.Instance, image *model.Image) error
	Status(ctx context.Context, inst *model.Instance) (string, error)
}

// Registry holds all available drivers and selects one on demand.
type Registry struct {
	mu      sync.RWMutex
	drivers map[string]Driver
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{drivers: make(map[string]Driver)}
}

// Register adds or replaces a driver by its Name().
func (r *Registry) Register(d Driver) {
	if d == nil {
		return
	}
	name := d.Name()
	if name == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.drivers[name] = d
}

// Get returns a driver by exact name.
func (r *Registry) Get(name string) (Driver, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.drivers[name]
	return d, ok
}

// List returns sorted driver names.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.drivers))
	for k := range r.drivers {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Drivers returns all registered drivers sorted by name.
func (r *Registry) Drivers() []Driver {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.drivers))
	for k := range r.drivers {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]Driver, 0, len(names))
	for _, n := range names {
		out = append(out, r.drivers[n])
	}
	return out
}

// Resolve returns the driver for preferred if it exists and probes successfully.
// If preferred is empty or unavailable, it tries each driver in sorted order
// and returns the first one that probes successfully.
// Returns error if no driver is available.
func (r *Registry) Resolve(ctx context.Context, preferred string) (Driver, error) {
	if preferred != "" {
		if d, ok := r.Get(preferred); ok {
			if err := d.Probe(ctx); err == nil {
				return d, nil
			}
		}
	}
	// Auto-select
	for _, d := range r.Drivers() {
		if err := d.Probe(ctx); err == nil {
			return d, nil
		}
	}
	if preferred != "" {
		return nil, fmt.Errorf("driver %q unavailable and no alternative available", preferred)
	}
	return nil, fmt.Errorf("no available driver")
}

// ProbeAll returns map of driver name -> probe error (nil means available).
func (r *Registry) ProbeAll(ctx context.Context) map[string]error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]error, len(r.drivers))
	for name, d := range r.drivers {
		out[name] = d.Probe(ctx)
	}
	return out
}

// DefaultRegistry returns a registry pre-loaded with all built-in drivers.
func DefaultRegistry() *Registry {
	reg := NewRegistry()
	reg.Register(NewMockDriver())
	reg.Register(NewIncusDriver())
	reg.Register(NewLxcDriver())
	reg.Register(NewQemuDriver())
	return reg
}

func jsonReader(b []byte) io.Reader { return bytes.NewReader(b) }

func containsFold(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}
