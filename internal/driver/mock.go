package driver

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/SakuraOpenSource/virtualis/internal/model"
)

// MockDriver is an in-memory driver used for development and tests.
// It stores instances in a map and simulates small operation delays.
type MockDriver struct {
	mu        sync.Mutex
	instances map[uint]*model.Instance
	delay     time.Duration
}

// NewMockDriver creates a mock driver with default delay.
func NewMockDriver() *MockDriver {
	return &MockDriver{
		instances: make(map[uint]*model.Instance),
		delay:     25 * time.Millisecond,
	}
}

// NewMockDriverWithDelay creates a mock with custom delay (useful for tests).
func NewMockDriverWithDelay(d time.Duration) *MockDriver {
	m := NewMockDriver()
	m.delay = d
	return m
}

func (m *MockDriver) Name() string { return "mock" }

func (m *MockDriver) Probe(_ context.Context) error { return nil }

func (m *MockDriver) sleep(ctx context.Context) error {
	if m.delay <= 0 {
		return nil
	}
	select {
	case <-time.After(m.delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *MockDriver) Create(ctx context.Context, inst *model.Instance) error {
	if inst == nil {
		return fmt.Errorf("nil instance")
	}
	if err := m.sleep(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *inst
	if cp.Status == "" {
		cp.Status = model.InstanceStatusStopped
	}
	m.instances[inst.ID] = &cp
	// reflect status back
	inst.Status = cp.Status
	return nil
}

func (m *MockDriver) Delete(ctx context.Context, inst *model.Instance) error {
	if inst == nil {
		return fmt.Errorf("nil instance")
	}
	if err := m.sleep(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.instances, inst.ID)
	return nil
}

func (m *MockDriver) Start(ctx context.Context, inst *model.Instance) error {
	if err := m.sleep(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if rec, ok := m.instances[inst.ID]; ok {
		rec.Status = model.InstanceStatusRunning
	}
	inst.Status = model.InstanceStatusRunning
	return nil
}

func (m *MockDriver) Stop(ctx context.Context, inst *model.Instance) error {
	if err := m.sleep(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if rec, ok := m.instances[inst.ID]; ok {
		rec.Status = model.InstanceStatusStopped
	}
	inst.Status = model.InstanceStatusStopped
	return nil
}

func (m *MockDriver) Restart(ctx context.Context, inst *model.Instance) error {
	if err := m.Stop(ctx, inst); err != nil {
		return err
	}
	return m.Start(ctx, inst)
}

func (m *MockDriver) HardStart(ctx context.Context, inst *model.Instance) error {
	return m.Start(ctx, inst)
}

func (m *MockDriver) HardStop(ctx context.Context, inst *model.Instance) error {
	return m.Stop(ctx, inst)
}

func (m *MockDriver) HardRestart(ctx context.Context, inst *model.Instance) error {
	return m.Restart(ctx, inst)
}

func (m *MockDriver) Reinstall(ctx context.Context, inst *model.Instance, _ *model.Image) error {
	if err := m.sleep(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if rec, ok := m.instances[inst.ID]; ok {
		rec.Status = model.InstanceStatusStopped
	}
	inst.Status = model.InstanceStatusStopped
	return nil
}

func (m *MockDriver) Status(ctx context.Context, inst *model.Instance) (string, error) {
	if err := m.sleep(ctx); err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if rec, ok := m.instances[inst.ID]; ok {
		return rec.Status, nil
	}
	if inst.Status != "" {
		return inst.Status, nil
	}
	return model.InstanceStatusStopped, nil
}

// Exists reports whether an instance is tracked.
func (m *MockDriver) Exists(id uint) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.instances[id]
	return ok
}

// Count returns number of tracked instances.
func (m *MockDriver) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.instances)
}
