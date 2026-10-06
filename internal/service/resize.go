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
}

func (s *VirtualisService) ResizeInstance(ctx context.Context, id uint, req ResizeInput) (result *model.Instance, err error) {
	guard, err := s.beginOperation(ctx, id, "resize")
	if err != nil {
		return nil, err
	}
	defer guard.finishInstance(&err, &result)
	ctx, cancel := context.WithTimeout(ctx, recoveryTimeout)
	defer cancel()
	inst, client, err := s.recoveryInstance(ctx, id, true)
	if err != nil {
		return nil, err
	}
	if req.Spec.CPUMilli < 0 || req.Spec.CPU < 1 && req.Spec.CPUMilli == 0 || req.Spec.MemoryMB < 1 || req.Spec.DiskGB < 1 {
		return nil, BadRequest("positive CPU, memory and disk required")
	}
	spec := req.Spec
	if spec.Arch == "" {
		spec.Arch = inst.Spec.Arch
	}
	spec, err = model.NormalizeInstanceSpec(spec)
	if err != nil {
		return nil, BadRequest("%s", err)
	}
	if spec.DiskGB < inst.Spec.DiskGB || canonicalArch(spec.Arch) != canonicalArch(inst.Spec.Arch) {
		return nil, BadRequest("disk shrinking and architecture changes are not supported")
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
