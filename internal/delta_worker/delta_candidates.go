package delta_worker

import (
	"context"
	"fmt"

	"github.com/containers/image/v5/docker/reference"
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

	tv, err := r.templateVersionFromEvent(ctx, ev)
	if err != nil {
		return DeltaCandidateResult{}, err
	}

	var candidates []DeltaCandidate
	for _, device := range devices {
		deviceCands, err := r.candidatesForDevice(ctx, device, tv)
		if err != nil {
			return DeltaCandidateResult{}, err
		}
		candidates = append(candidates, deviceCands...)
	}
	if len(candidates) == 0 {
		return DeltaCandidateResult{Skip: true}, nil
	}
	return DeltaCandidateResult{Candidates: candidates}, nil
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

	candidates, err := r.candidatesForDevice(ctx, device, nil)
	if err != nil {
		return DeltaCandidateResult{}, err
	}
	if len(candidates) == 0 {
		return DeltaCandidateResult{Skip: true}, nil
	}
	return DeltaCandidateResult{Candidates: candidates}, nil
}

func (r *Resolver) templateVersionFromEvent(ctx context.Context, ev worker_client.EventWithOrgId) (*domain.TemplateVersion, error) {
	if r.TemplateVersion == nil {
		return nil, fmt.Errorf("template version loader is required")
	}
	if ev.Event.Details == nil {
		return nil, fmt.Errorf("prepare deltas event is missing details")
	}
	details, err := ev.Event.Details.AsPrepareDeltasDetails()
	if err != nil {
		return nil, fmt.Errorf("prepare deltas details: %w", err)
	}
	if details.TemplateVersion == nil || *details.TemplateVersion == "" {
		return nil, fmt.Errorf("fleet prepare deltas event requires templateVersion")
	}
	return r.TemplateVersion(ctx, ev.OrgId, ev.Event.InvolvedObject.Name, *details.TemplateVersion)
}

func (r *Resolver) candidatesForDevice(ctx context.Context, device *domain.Device, tv *domain.TemplateVersion) ([]DeltaCandidate, error) {
	if !deviceEligible(device) {
		return nil, nil
	}
	if currentDigest(device) == "" && r.Expand == nil {
		return nil, nil
	}

	spec, err := r.desiredSpec(device, tv)
	if err != nil {
		return nil, nil
	}
	if spec == nil {
		return nil, nil
	}

	if r.Render == nil {
		return nil, fmt.Errorf("render is required")
	}
	rendered, err := r.Render(ctx, spec)
	if err != nil {
		return nil, nil
	}

	var candidates []DeltaCandidate
	if cand, ok, err := r.osCandidate(ctx, device, rendered); err != nil {
		return nil, err
	} else if ok {
		candidates = append(candidates, cand)
	}
	if r.Expand != nil {
		candidates = r.Expand(rendered, candidates)
	}
	return candidates, nil
}

func (r *Resolver) desiredSpec(device *domain.Device, tv *domain.TemplateVersion) (*domain.DeviceSpec, error) {
	if tv == nil {
		return device.Spec, nil
	}
	desired := r.DesiredSpec
	if desired == nil {
		desired = tasks.DesiredSpecFromTemplate
	}
	return desired(device, tv)
}

func (r *Resolver) osCandidate(ctx context.Context, device *domain.Device, rendered tasks.RenderedSpec) (DeltaCandidate, bool, error) {
	current := currentDigest(device)
	if current == "" || rendered.OsImage == "" {
		return DeltaCandidate{}, false, nil
	}
	if r.Inspect == nil {
		return DeltaCandidate{}, false, fmt.Errorf("inspect is required")
	}
	newDigest, err := r.Inspect(ctx, rendered.OsImage)
	if err != nil {
		return DeltaCandidate{}, false, err
	}
	repo, err := imageRepository(rendered.OsImage)
	if err != nil {
		return DeltaCandidate{}, false, err
	}
	return DeltaCandidate{
		ImageRepository: repo,
		CurrentDigest:   current,
		NewDigest:       newDigest,
	}, true, nil
}

func imageRepository(osImage string) (string, error) {
	named, err := reference.ParseNormalizedNamed(osImage)
	if err != nil {
		return "", fmt.Errorf("parse os image %q: %w", osImage, err)
	}
	return named.Name(), nil
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
		if deviceEligible(d) {
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
