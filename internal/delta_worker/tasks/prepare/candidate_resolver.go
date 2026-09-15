package prepare

import (
	"context"
	"fmt"
	"net/http"

	"github.com/containers/image/v5/docker/reference"
	deltaconfig "github.com/flightctl/flightctl/internal/delta_worker/config"
	generateTask "github.com/flightctl/flightctl/internal/delta_worker/tasks/generate"
	"github.com/flightctl/flightctl/internal/domain"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	repositoryservice "github.com/flightctl/flightctl/internal/service/repository"
	templateversionservice "github.com/flightctl/flightctl/internal/service/templateversion"
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
	Fleet      *domain.Fleet
}

type Resolver struct {
	FleetService           fleetservice.Service
	DeviceService          deviceservice.Service
	RepositoryService      repositoryservice.Service
	TemplateVersionService templateversionservice.Service
	Config                 *deltaconfig.DeltaGenerationConfig

	Inspect func(ctx context.Context, orgId uuid.UUID, image string) (string, error)
	Render  func(ctx context.Context, orgId uuid.UUID, spec *domain.DeviceSpec) (tasks.RenderedSpec, error)
	Expand  func(context.Context, uuid.UUID, *domain.Device, tasks.RenderedSpec, []DeltaCandidate) []DeltaCandidate
}

func (r *Resolver) DeltaCandidates(ctx context.Context, ev worker_client.EventWithOrgId) (DeltaCandidateResult, error) {
	limit := int32(1)
	fieldSelector := "spec.deltaStorageTarget=true"
	repositories, status := r.RepositoryService.ListRepositories(ctx, ev.OrgId, domain.ListRepositoriesParams{
		Limit:         &limit,
		FieldSelector: &fieldSelector,
	})
	if status.Code != http.StatusOK {
		return DeltaCandidateResult{}, fmt.Errorf("list delta storage target repositories: %s", status.Message)
	}

	if (repositories == nil || len(repositories.Items) == 0) && generateTask.WriteSpecFromConfig(r.Config) == nil {
		return DeltaCandidateResult{Skip: true}, nil
	}

	switch ev.Event.InvolvedObject.Kind {
	case domain.FleetKind:
		return r.candidatesForFleetEvent(ctx, ev)
	case domain.DeviceKind:
		return r.candidatesForDeviceEvent(ctx, ev)
	default:
		return DeltaCandidateResult{}, fmt.Errorf("unsupported involved object kind %q", ev.Event.InvolvedObject.Kind)
	}
}

func (r *Resolver) candidatesForFleetEvent(ctx context.Context, ev worker_client.EventWithOrgId) (DeltaCandidateResult, error) {
	eventTemplateVersion, err := prepareEventTemplateVersion(ev)
	if err != nil {
		return DeltaCandidateResult{}, err
	}
	tv, status := r.TemplateVersionService.GetTemplateVersion(ctx, ev.OrgId, ev.Event.InvolvedObject.Name, *eventTemplateVersion)
	if status.Code != http.StatusOK {
		return DeltaCandidateResult{}, fmt.Errorf("get template version %s/%s/%s: %s", ev.OrgId, ev.Event.InvolvedObject.Name, *eventTemplateVersion, status.Message)
	}
	if tv == nil {
		return DeltaCandidateResult{}, fmt.Errorf("get template version %s/%s/%s returned no resource", ev.OrgId, ev.Event.InvolvedObject.Name, *eventTemplateVersion)
	}
	if tv.Spec.Fleet == "" {
		return DeltaCandidateResult{}, fmt.Errorf("template version %q has no owning fleet", *eventTemplateVersion)
	}
	if tv.Spec.Fleet != ev.Event.InvolvedObject.Name {
		return DeltaCandidateResult{}, fmt.Errorf("template version %q belongs to fleet %q, event targets fleet %q", *eventTemplateVersion, tv.Spec.Fleet, ev.Event.InvolvedObject.Name)
	}

	fleet, status := r.FleetService.GetFleet(ctx, ev.OrgId, tv.Spec.Fleet, domain.GetFleetParams{})
	if status.Code != http.StatusOK {
		return DeltaCandidateResult{}, fmt.Errorf("get fleet %s/%s: %s", ev.OrgId, tv.Spec.Fleet, status.Message)
	}
	currentTemplateVersion, ok := fleetTemplateVersion(fleet)
	if !ok || currentTemplateVersion != *eventTemplateVersion {
		return DeltaCandidateResult{}, fmt.Errorf("fleet %s template version changed: event=%q current=%q", ev.Event.InvolvedObject.Name, *eventTemplateVersion, currentTemplateVersion)
	}
	if fleet.Spec.RolloutPolicy != nil && fleet.Spec.RolloutPolicy.DeltaGeneration != nil && fleet.Spec.RolloutPolicy.DeltaGeneration.GenerateDelta != nil && !*fleet.Spec.RolloutPolicy.DeltaGeneration.GenerateDelta {
		return DeltaCandidateResult{Skip: true, Fleet: fleet}, nil
	}

	limit := int32(tasks.ItemsPerPage)
	owner := util.SetResourceOwner(domain.FleetKind, tv.Spec.Fleet)
	fieldSelector := fmt.Sprintf("metadata.owner=%s,status.systemInfo.deltaEligible=true", *owner)
	params := domain.ListDevicesParams{Limit: &limit, FieldSelector: &fieldSelector}

	seen := make(map[DeltaCandidate]struct{})
	var candidates []DeltaCandidate
	for {
		list, status := r.DeviceService.ListDevices(ctx, ev.OrgId, params, nil)
		if status.Code != http.StatusOK {
			return DeltaCandidateResult{}, fmt.Errorf("list devices for owner %s: %s", *owner, status.Message)
		}
		if list == nil {
			return DeltaCandidateResult{}, fmt.Errorf("list devices for owner %s returned no response", *owner)
		}

		for i := range list.Items {
			deviceCands, err := r.candidatesForDevice(ctx, ev.OrgId, &list.Items[i], tv)
			if err != nil {
				return DeltaCandidateResult{}, err
			}
			for _, candidate := range deviceCands {
				if _, ok := seen[candidate]; ok {
					continue
				}
				seen[candidate] = struct{}{}
				candidates = append(candidates, candidate)
			}
		}

		if list.Metadata.Continue == nil {
			break
		}
		params.Continue = list.Metadata.Continue
	}
	if len(candidates) == 0 {
		return DeltaCandidateResult{Skip: true, Fleet: fleet}, nil
	}
	return DeltaCandidateResult{Candidates: candidates, Fleet: fleet}, nil
}

func (r *Resolver) candidatesForDeviceEvent(ctx context.Context, ev worker_client.EventWithOrgId) (DeltaCandidateResult, error) {
	device, status := r.DeviceService.GetDevice(ctx, ev.OrgId, ev.Event.InvolvedObject.Name)
	if status.Code != http.StatusOK {
		return DeltaCandidateResult{}, fmt.Errorf("get device %s/%s: %s", ev.OrgId, ev.Event.InvolvedObject.Name, status.Message)
	}
	expectedSpecHash, err := deviceSpecHashFromEvent(ev)
	if err != nil {
		return DeltaCandidateResult{}, err
	}
	if actualSpecHash := device.SpecHash(); actualSpecHash != expectedSpecHash {
		return DeltaCandidateResult{}, fmt.Errorf("device %s spec hash changed: event=%q current=%q", ev.Event.InvolvedObject.Name, expectedSpecHash, actualSpecHash)
	}
	if !deviceEligible(device) {
		return DeltaCandidateResult{Skip: true}, nil
	}

	candidates, err := r.candidatesForDevice(ctx, ev.OrgId, device, nil)
	if err != nil {
		return DeltaCandidateResult{}, err
	}
	if len(candidates) == 0 {
		return DeltaCandidateResult{Skip: true}, nil
	}
	return DeltaCandidateResult{Candidates: dedupCandidates(candidates)}, nil
}

func prepareEventTemplateVersion(ev worker_client.EventWithOrgId) (*string, error) {
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
	return details.TemplateVersion, nil
}

func fleetTemplateVersion(fleet *domain.Fleet) (string, bool) {
	if fleet == nil || fleet.Metadata.Annotations == nil {
		return "", false
	}
	templateVersion, ok := (*fleet.Metadata.Annotations)[domain.FleetAnnotationTemplateVersion]
	return templateVersion, ok && templateVersion != ""
}

func deviceSpecHashFromEvent(ev worker_client.EventWithOrgId) (string, error) {
	if ev.Event.Details == nil {
		return "", fmt.Errorf("prepare deltas event is missing details")
	}
	details, err := ev.Event.Details.AsPrepareDeltasDetails()
	if err != nil {
		return "", fmt.Errorf("prepare deltas details: %w", err)
	}
	if details.SpecHash == nil || *details.SpecHash == "" {
		return "", fmt.Errorf("device prepare deltas event requires specHash")
	}
	return *details.SpecHash, nil
}

func (r *Resolver) candidatesForDevice(ctx context.Context, orgId uuid.UUID, device *domain.Device, tv *domain.TemplateVersion) ([]DeltaCandidate, error) {
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
	rendered, err := r.Render(ctx, orgId, spec)
	if err != nil {
		return nil, nil
	}

	var candidates []DeltaCandidate
	if cand, ok, err := r.osCandidate(ctx, orgId, device, rendered); err != nil {
		return nil, err
	} else if ok {
		candidates = append(candidates, cand)
	}
	if r.Expand != nil {
		candidates = r.Expand(ctx, orgId, device, rendered, candidates)
	}
	return candidates, nil
}

func (r *Resolver) desiredSpec(device *domain.Device, tv *domain.TemplateVersion) (*domain.DeviceSpec, error) {
	if tv == nil {
		return device.Spec, nil
	}
	return tasks.DesiredSpecFromTemplate(device, tv)
}

func (r *Resolver) osCandidate(ctx context.Context, orgId uuid.UUID, device *domain.Device, rendered tasks.RenderedSpec) (DeltaCandidate, bool, error) {
	current := currentDigest(device)
	if current == "" || rendered.OsImage == "" {
		return DeltaCandidate{}, false, nil
	}
	repo, err := imageRepository(rendered.OsImage)
	if err != nil {
		return DeltaCandidate{}, false, nil
	}
	if r.Inspect == nil {
		return DeltaCandidate{}, false, fmt.Errorf("inspect is required")
	}
	newDigest, err := r.Inspect(ctx, orgId, rendered.OsImage)
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

func deviceEligible(d *domain.Device) bool {
	if d == nil || d.Status == nil {
		return false
	}
	if d.Status.SystemInfo.DeltaEligible == nil || !*d.Status.SystemInfo.DeltaEligible {
		return false
	}
	return d.Status.SystemInfo.BootcVersion != nil && *d.Status.SystemInfo.BootcVersion != ""
}

func currentDigest(d *domain.Device) string {
	if d == nil || d.Status == nil {
		return ""
	}
	return d.Status.Os.ImageDigest
}

func dedupCandidates(cands []DeltaCandidate) []DeltaCandidate {
	seen := make(map[string]struct{}, len(cands))
	out := make([]DeltaCandidate, 0, len(cands))
	for _, c := range cands {
		key := c.ImageRepository + "\x00" + c.CurrentDigest + "\x00" + c.NewDigest
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, c)
	}
	return out
}
