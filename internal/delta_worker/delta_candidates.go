package delta_worker

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/google/uuid"
)

type DeltaCandidate struct {
	ImageRepository string
	CurrentDigest   string
	NewDigest       string
}

type DeltaCandidateResult struct {
	Candidates []DeltaCandidate
	Skip       bool
}

type Resolver struct {
	Fleet           func(ctx context.Context, orgId uuid.UUID, name string) (*domain.Fleet, error)
	TemplateVersion func(ctx context.Context, orgId uuid.UUID, fleet, name string) (*domain.TemplateVersion, error)
	Devices         func(ctx context.Context, orgId uuid.UUID, owner string) ([]*domain.Device, error)
	Device          func(ctx context.Context, orgId uuid.UUID, name string) (*domain.Device, error)
	WriteTarget     func(ctx context.Context, orgId uuid.UUID) (*domain.OciRepoSpec, error)
	Inspect         func(ctx context.Context, image string) (string, error)
	DesiredSpec     func(device *domain.Device, tv *domain.TemplateVersion) (*domain.DeviceSpec, error)
	Render          func(ctx context.Context, spec *domain.DeviceSpec) (tasks.RenderedSpec, error)
	Expand          func(tasks.RenderedSpec, []DeltaCandidate) []DeltaCandidate
}

func (r *Resolver) DeltaCandidates(ctx context.Context, ev worker_client.EventWithOrgId) (DeltaCandidateResult, error) {
	if r.WriteTarget == nil {
		return DeltaCandidateResult{}, fmt.Errorf("write target loader is required")
	}
	target, err := r.WriteTarget(ctx, ev.OrgId)
	if err != nil {
		return DeltaCandidateResult{}, err
	}
	if target == nil {
		return DeltaCandidateResult{Skip: true}, nil
	}

	switch ev.Event.InvolvedObject.Kind {
	case domain.FleetKind:
		return r.fleetCandidates(ctx, ev)
	case domain.DeviceKind:
		return r.deviceCandidates(ctx, ev)
	default:
		return DeltaCandidateResult{}, fmt.Errorf("unsupported involved object kind %q", ev.Event.InvolvedObject.Kind)
	}
}

func (r *Resolver) fleetCandidates(ctx context.Context, ev worker_client.EventWithOrgId) (DeltaCandidateResult, error) {
	if r.Fleet == nil {
		return DeltaCandidateResult{}, fmt.Errorf("fleet loader is required")
	}
	fleet, err := r.Fleet(ctx, ev.OrgId, ev.Event.InvolvedObject.Name)
	if err != nil {
		return DeltaCandidateResult{}, err
	}
	if fleet.Spec.RolloutPolicy != nil && fleet.Spec.RolloutPolicy.GenerateDelta != nil && !*fleet.Spec.RolloutPolicy.GenerateDelta {
		return DeltaCandidateResult{Skip: true}, nil
	}

	devices, err := r.listFleetDevices(ctx, ev.OrgId, ev.Event.InvolvedObject.Name)
	if err != nil {
		return DeltaCandidateResult{}, err
	}
	if !hasEligibleDevice(devices) {
		return DeltaCandidateResult{Skip: true}, nil
	}
	return DeltaCandidateResult{Skip: true}, nil
}

func (r *Resolver) deviceCandidates(ctx context.Context, ev worker_client.EventWithOrgId) (DeltaCandidateResult, error) {
	if r.Device == nil {
		return DeltaCandidateResult{}, fmt.Errorf("device loader is required")
	}
	device, err := r.Device(ctx, ev.OrgId, ev.Event.InvolvedObject.Name)
	if err != nil {
		return DeltaCandidateResult{}, err
	}
	if !deviceEligible(device) {
		return DeltaCandidateResult{Skip: true}, nil
	}
	return DeltaCandidateResult{Skip: true}, nil
}

func (r *Resolver) listFleetDevices(ctx context.Context, orgId uuid.UUID, fleetName string) ([]*domain.Device, error) {
	if r.Devices == nil {
		return nil, fmt.Errorf("device list loader is required")
	}
	owner := util.SetResourceOwner(domain.FleetKind, fleetName)
	return r.Devices(ctx, orgId, *owner)
}

func hasEligibleDevice(devices []*domain.Device) bool {
	for _, d := range devices {
		if deviceEligible(d) && currentDigest(d) != "" {
			return true
		}
	}
	return false
}

func deviceEligible(d *domain.Device) bool {
	return d != nil && d.Status != nil && d.Status.Capabilities != nil &&
		d.Status.Capabilities.DeltaEligible != nil && *d.Status.Capabilities.DeltaEligible
}

func currentDigest(d *domain.Device) string {
	if d == nil || d.Status == nil {
		return ""
	}
	return d.Status.Os.ImageDigest
}
