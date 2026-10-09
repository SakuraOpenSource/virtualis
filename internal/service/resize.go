package service

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/SakuraOpenSource/virtualis/internal/model"
	"reflect"
)

type ResizeInput struct {
	Spec    model.InstanceSpec   `json:"spec"`
	Network *model.NetworkConfig `json:"network,omitempty"`
	// CallerRef carries the upstream correlation id (X-Levis-Operation-ID)
	// into the persisted operation logs. It is set by the transport layer,
	// never deserialized from the body, hence json:"-".
	CallerRef string `json:"-"`
}

// minResizeMemoryMB is the shared memory floor. The agent's ValidateResize
// rejects anything below 128 MB before touching the runtime; the master must
// enforce the same boundary up-front, otherwise a predictable 400 from the
// agent arrives AFTER a fence was taken and (being a client.Resize error) it
// retains a durable lock that blocks every retry and even status calls.
const minResizeMemoryMB = 128

// validateResizeSpec checks the request against the instance without touching
// the agent or the database fence. It must run before beginOperation so
// parameter errors are plain 400s with no persistent side effects.
func validateResizeSpec(req ResizeInput, inst *model.Instance) (model.InstanceSpec, error) {
	if req.Spec.CPUMilli < 0 || req.Spec.CPU < 1 && req.Spec.CPUMilli == 0 || req.Spec.MemoryMB < 1 || req.Spec.DiskGB < 1 {
		return req.Spec, BadRequest("positive CPU, memory and disk required")
	}
	spec := req.Spec
	if spec.Arch == "" {
		spec.Arch = inst.Spec.Arch
	}
	spec, err := model.NormalizeInstanceSpec(spec)
	if err != nil {
		return spec, BadRequest("%s", err)
	}
	if spec.MemoryMB < minResizeMemoryMB {
		return spec, BadRequest("memory must be at least %d MB", minResizeMemoryMB)
	}
	if spec.DiskGB < inst.Spec.DiskGB || canonicalArch(spec.Arch) != canonicalArch(inst.Spec.Arch) {
		return spec, BadRequest("disk shrinking and architecture changes are not supported")
	}
	return spec, nil
}

func (s *VirtualisService) ResizeInstance(ctx context.Context, id uint, req ResizeInput) (result *model.Instance, err error) {
	inst, err := s.GetInstance(id)
	if err != nil {
		return nil, err
	}
	// Parameter validation runs BEFORE the fence is taken: an invalid spec is
	// a plain 400 that the caller can fix and retry, and it must not leave a
	// durable busy fence behind (the agent would reject it anyway, but only
	// after master-side state had been mutated).
	spec, err := validateResizeSpec(req, inst)
	if err != nil {
		return nil, err
	}
	guard, err := s.beginOperationWithRef(ctx, id, "resize", req.CallerRef)
	if err != nil {
		return nil, err
	}
	if guard.replay {
		// Idempotent retry of a completed resize: return the recorded
		// outcome instead of re-applying resources.
		return s.GetInstance(id)
	}
	defer guard.finishInstance(&err, &result)
	ctx, cancel := context.WithTimeout(ctx, recoveryTimeout)
	defer cancel()
	inst, client, err := s.recoveryInstance(ctx, id, true)
	if err != nil {
		return nil, err
	}
	// REV-RESIZE-TOCTOU: re-validate the spec against the CURRENT row read
	// under the fence. The fence-external validation above ran against a
	// row that may already have been stale (a competing resize completing
	// between the first read and beginOperation); sending its result to the
	// agent would turn the agent's defensive 400 into an uncertain
	// master-side failure that retains a durable fence. Re-rejecting here
	// is a plain 400 with no RPC and the fence released above.
	spec, err = validateResizeSpec(req, inst)
	if err != nil {
		return nil, err
	}
	network := inst.Network
	if req.Network != nil {
		// Resize changes quotas only. Topology belongs to network/migration APIs.
		n := req.Network
		if n.Mode != "" && n.Mode != network.Mode || n.IPv4 != "" && n.IPv4 != network.IPv4 || n.Bridge != "" && n.Bridge != network.Bridge || n.MAC != "" && n.MAC != network.MAC || n.Gateway != "" && n.Gateway != network.Gateway || len(n.DNS) > 0 && !reflect.DeepEqual(n.DNS, network.DNS) {
			return nil, BadRequest("resize cannot change network ownership or topology")
		}
		network.BandwidthMbps = n.BandwidthMbps
		network.TrafficGB = n.TrafficGB
	}
	network, err = model.NormalizeNetworkConfig(network)
	if err != nil {
		return nil, BadRequest("%s", err)
	}
	if reflect.DeepEqual(spec, inst.Spec) && reflect.DeepEqual(network, inst.Network) {
		return inst, nil
	}
	if err = guard.phase("resize", "applying stopped runtime resources", nil); err != nil {
		return nil, err
	}
	remote, err := client.Resize(ctx, toWireInstance(inst, inst.Image), spec, network)
	if err != nil {
		guard.retain = true
		return nil, agentFailure(err)
	}
	if !reflect.DeepEqual(remote.Spec, spec) || !reflect.DeepEqual(remote.Network, network) {
		guard.retain = true
		return nil, Conflict("Agent returned different resources; reconciliation required")
	}
	specJSON, _ := json.Marshal(spec)
	networkJSON, _ := json.Marshal(network)
	res := s.db.Model(inst).Where("busy_operation = ?", guard.token).Updates(map[string]any{"spec": string(specJSON), "network": string(networkJSON), "status": model.InstanceStatusStopped})
	if res.Error != nil || res.RowsAffected != 1 {
		guard.retain = true
		return nil, errors.Join(res.Error, Conflict("runtime resized but database switch needs reconciliation"))
	}
	return s.GetInstance(id)
}
