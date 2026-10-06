package prepare

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/containers/image/v5/docker/reference"
	deltaconfig "github.com/flightctl/flightctl/internal/delta_worker/config"
	generateTask "github.com/flightctl/flightctl/internal/delta_worker/tasks/generate"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/oci"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	repositoryservice "github.com/flightctl/flightctl/internal/service/repository"
	templateversionservice "github.com/flightctl/flightctl/internal/service/templateversion"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/google/uuid"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sirupsen/logrus"
)

type DeltaCandidate struct {
	ImageRepository string
	CurrentDigest   string
	NewDigest       string
}

type DeltaCandidateResult struct {
	Candidates []DeltaCandidate
	Skip       bool
	Superseded bool
	Fleet      *domain.Fleet
}

type Resolver struct {
	FleetService           fleetservice.Service
	DeviceService          deviceservice.Service
	RepositoryService      repositoryservice.Service
	TemplateVersionService templateversionservice.Service
	Config                 *deltaconfig.DeltaGenerationConfig
	Log                    logrus.FieldLogger

	Inspect          func(ctx context.Context, orgId uuid.UUID, image string) (string, error)
	InspectForSource func(
		ctx context.Context,
		orgId uuid.UUID,
		image string,
		sourceDigest string,
		fallbackPlatform *ocispec.Platform,
	) (resolvedSourceDigest, targetDigest string, err error)
	Render func(ctx context.Context, orgId uuid.UUID, device *domain.Device, spec *domain.DeviceSpec) (tasks.RenderedSpec, error)
	Expand func(context.Context, uuid.UUID, *domain.Device, tasks.RenderedSpec, []DeltaCandidate) []DeltaCandidate
}

func (r *Resolver) DeltaCandidates(ctx context.Context, ev worker_client.EventWithOrgId) (DeltaCandidateResult, error) {
	var (
		fleetTemplate *domain.TemplateVersion
		fleet         *domain.Fleet
	)
	if ev.Event.InvolvedObject.Kind == domain.FleetKind {
		var superseded bool
		var err error
		fleetTemplate, fleet, superseded, err = r.currentFleetTemplateVersion(ctx, ev)
		if err != nil {
			return DeltaCandidateResult{}, err
		}
		if superseded {
			return DeltaCandidateResult{Superseded: true}, nil
		}
	}

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
		return DeltaCandidateResult{Skip: true, Fleet: fleet}, nil
	}

	switch ev.Event.InvolvedObject.Kind {
	case domain.FleetKind:
		return r.candidatesForFleetEvent(ctx, ev, fleetTemplate, fleet)
	case domain.DeviceKind:
		return r.candidatesForDeviceEvent(ctx, ev)
	default:
		return DeltaCandidateResult{}, fmt.Errorf("unsupported involved object kind %q", ev.Event.InvolvedObject.Kind)
	}
}

func (r *Resolver) currentFleetTemplateVersion(ctx context.Context, ev worker_client.EventWithOrgId) (*domain.TemplateVersion, *domain.Fleet, bool, error) {
	eventTemplateVersion, err := prepareEventTemplateVersion(ev)
	if err != nil {
		return nil, nil, false, err
	}
	if r.TemplateVersionService == nil {
		return nil, nil, false, fmt.Errorf("template version service is required for fleet prepare events")
	}
	tv, status := r.TemplateVersionService.GetTemplateVersion(ctx, ev.OrgId, ev.Event.InvolvedObject.Name, *eventTemplateVersion)
	if status.Code == http.StatusNotFound {
		return nil, nil, true, nil
	}
	if status.Code != http.StatusOK {
		return nil, nil, false, fmt.Errorf("get template version %s/%s/%s: %s", ev.OrgId, ev.Event.InvolvedObject.Name, *eventTemplateVersion, status.Message)
	}
	if tv == nil {
		return nil, nil, false, fmt.Errorf("get template version %s/%s/%s returned no resource", ev.OrgId, ev.Event.InvolvedObject.Name, *eventTemplateVersion)
	}
	if tv.Spec.Fleet == "" {
		return nil, nil, false, fmt.Errorf("template version %q has no owning fleet", *eventTemplateVersion)
	}
	if tv.Spec.Fleet != ev.Event.InvolvedObject.Name {
		return nil, nil, false, fmt.Errorf("template version %q belongs to fleet %q, event targets fleet %q", *eventTemplateVersion, tv.Spec.Fleet, ev.Event.InvolvedObject.Name)
	}

	latest, status := r.TemplateVersionService.GetLatestTemplateVersion(ctx, ev.OrgId, ev.Event.InvolvedObject.Name)
	if status.Code == http.StatusNotFound {
		return nil, nil, true, nil
	}
	if status.Code != http.StatusOK {
		return nil, nil, false, fmt.Errorf("get latest template version for fleet %s/%s: %s", ev.OrgId, ev.Event.InvolvedObject.Name, status.Message)
	}
	if latest == nil || latest.Metadata.Name == nil || *latest.Metadata.Name == "" {
		return nil, nil, false, fmt.Errorf("get latest template version for fleet %s/%s returned no named resource", ev.OrgId, ev.Event.InvolvedObject.Name)
	}
	if *latest.Metadata.Name != *eventTemplateVersion {
		return nil, nil, true, nil
	}
	if latest.Spec.Fleet != "" && latest.Spec.Fleet != ev.Event.InvolvedObject.Name {
		return nil, nil, false, fmt.Errorf("latest template version %q belongs to fleet %q, event targets fleet %q", *latest.Metadata.Name, latest.Spec.Fleet, ev.Event.InvolvedObject.Name)
	}

	if r.FleetService == nil {
		return nil, nil, false, fmt.Errorf("fleet service is required for fleet prepare events")
	}
	fleet, status := r.FleetService.GetFleet(ctx, ev.OrgId, tv.Spec.Fleet, domain.GetFleetParams{})
	if status.Code != http.StatusOK {
		return nil, nil, false, fmt.Errorf("get fleet %s/%s: %s", ev.OrgId, tv.Spec.Fleet, status.Message)
	}
	if fleet == nil {
		return nil, nil, false, fmt.Errorf("get fleet %s/%s returned no resource", ev.OrgId, ev.Event.InvolvedObject.Name)
	}
	return tv, fleet, false, nil
}

func (r *Resolver) candidatesForFleetEvent(ctx context.Context, ev worker_client.EventWithOrgId, tv *domain.TemplateVersion, fleet *domain.Fleet) (DeltaCandidateResult, error) {
	if fleet.Spec.RolloutPolicy != nil && fleet.Spec.RolloutPolicy.DeltaGeneration != nil && fleet.Spec.RolloutPolicy.DeltaGeneration.GenerateDelta != nil && !*fleet.Spec.RolloutPolicy.DeltaGeneration.GenerateDelta {
		return DeltaCandidateResult{Skip: true, Fleet: fleet}, nil
	}

	limit := int32(tasks.ItemsPerPage)
	owner := util.SetResourceOwner(domain.FleetKind, ev.Event.InvolvedObject.Name)
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
	if status.Code == http.StatusNotFound {
		return DeltaCandidateResult{Superseded: true}, nil
	}
	if status.Code != http.StatusOK {
		return DeltaCandidateResult{}, fmt.Errorf("get device %s/%s: %s", ev.OrgId, ev.Event.InvolvedObject.Name, status.Message)
	}
	expectedGeneration, err := deviceGenerationFromEvent(ev)
	if err != nil {
		return DeltaCandidateResult{}, err
	}
	// Rendering is held until preparation finishes, so the rendered spec hash
	// can still describe an earlier desired spec. Generation changes immediately.
	if device == nil {
		if r.Log != nil {
			r.Log.Debugf("Dropping prepare deltas event for device %s/%s at generation %d: device no longer exists", ev.OrgId, ev.Event.InvolvedObject.Name, expectedGeneration)
		}
		return DeltaCandidateResult{Superseded: true}, nil
	}
	if device.Metadata.Generation == nil || *device.Metadata.Generation != expectedGeneration {
		if r.Log != nil {
			var currentGeneration any
			if device.Metadata.Generation != nil {
				currentGeneration = *device.Metadata.Generation
			}
			r.Log.WithField("currentGeneration", currentGeneration).Debugf("Dropping superseded prepare deltas event for device %s/%s at generation %d", ev.OrgId, ev.Event.InvolvedObject.Name, expectedGeneration)
		}
		return DeltaCandidateResult{Superseded: true}, nil
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

func deviceGenerationFromEvent(ev worker_client.EventWithOrgId) (int64, error) {
	if ev.Event.Details == nil {
		return 0, fmt.Errorf("prepare deltas event is missing details")
	}
	details, err := ev.Event.Details.AsPrepareDeltasDetails()
	if err != nil {
		return 0, fmt.Errorf("prepare deltas details: %w", err)
	}
	if details.Generation == nil || *details.Generation <= 0 {
		return 0, fmt.Errorf("device prepare deltas event requires a positive generation")
	}
	return *details.Generation, nil
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
	rendered, err := r.Render(ctx, orgId, device, spec)
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
	if r.Inspect == nil && r.InspectForSource == nil {
		return DeltaCandidate{}, false, fmt.Errorf("inspect is required")
	}
	resolvedSource, newDigest := current, ""
	if r.InspectForSource != nil {
		resolvedSource, newDigest, err = r.InspectForSource(ctx, orgId, rendered.OsImage, current, oci.DeviceImagePlatform(device))
	} else {
		newDigest, err = r.Inspect(ctx, orgId, rendered.OsImage)
	}
	if err != nil {
		if errors.Is(err, oci.ErrSourceDigestUnresolved) {
			return DeltaCandidate{}, false, nil
		}
		return DeltaCandidate{}, false, err
	}
	if resolvedSource == "" || newDigest == "" || resolvedSource == newDigest {
		return DeltaCandidate{}, false, nil
	}
	return DeltaCandidate{
		ImageRepository: repo,
		CurrentDigest:   resolvedSource,
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
