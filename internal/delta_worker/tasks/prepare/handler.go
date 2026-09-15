package prepare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltageneration"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltaprepare"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltapreparegeneration"
	deltastore "github.com/flightctl/flightctl/internal/delta_worker/store"
	generateTask "github.com/flightctl/flightctl/internal/delta_worker/tasks/generate"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	eventservice "github.com/flightctl/flightctl/internal/service/events"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	templateversionservice "github.com/flightctl/flightctl/internal/service/templateversion"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/google/uuid"
	"github.com/samber/lo"
)

type Handler struct {
	resolver                 *Resolver
	emit                     func(ctx context.Context, orgId uuid.UUID, event *domain.Event) error
	Now                      func() time.Time
	MaxWaitForDelta          *time.Duration
	DeltaGenerationTimeout   time.Duration
	prepareService           deltaprepare.Service
	generationService        deltageneration.Service
	prepareGenerationService deltapreparegeneration.Service
	// Exported service fields keep the handler constructible by integration
	// harnesses while the worker constructor continues to validate dependencies.
	PrepareService           deltaprepare.Service
	GenerationService        deltageneration.Service
	PrepareGenerationService deltapreparegeneration.Service
	Events                   eventservice.Service
	FleetSvc                 fleetservice.Service
	DeviceSvc                deviceservice.Service
	TVSvc                    templateversionservice.Service
}

type prepareIdentity struct {
	templateVersion *string
	specHash        *string
	resourceVersion int64
}

func NewHandler(
	resolver *Resolver,
	emit func(ctx context.Context, orgID uuid.UUID, event *domain.Event) error,
	prepareService deltaprepare.Service,
	generationService deltageneration.Service,
	prepareGenerationService deltapreparegeneration.Service,
) (*Handler, error) {
	if resolver == nil {
		return nil, fmt.Errorf("resolver is required")
	}
	if resolver.FleetService == nil {
		return nil, fmt.Errorf("fleet service is required")
	}
	if resolver.DeviceService == nil {
		return nil, fmt.Errorf("device service is required")
	}
	if resolver.RepositoryService == nil {
		return nil, fmt.Errorf("repository service is required")
	}
	if resolver.Config == nil {
		return nil, fmt.Errorf("delta worker config is required")
	}
	if resolver.TemplateVersionService == nil {
		return nil, fmt.Errorf("template version service is required")
	}
	if prepareService == nil || generationService == nil || prepareGenerationService == nil {
		return nil, fmt.Errorf("delta services are required")
	}
	return &Handler{
		resolver:                 resolver,
		emit:                     emit,
		Now:                      time.Now,
		prepareService:           prepareService,
		generationService:        generationService,
		prepareGenerationService: prepareGenerationService,
		PrepareService:           prepareService,
		GenerationService:        generationService,
		PrepareGenerationService: prepareGenerationService,
	}, nil
}

func (p *Handler) normalizeServices() {
	if p.prepareService == nil {
		p.prepareService = p.PrepareService
	}
	if p.generationService == nil {
		p.generationService = p.GenerationService
	}
	if p.prepareGenerationService == nil {
		p.prepareGenerationService = p.PrepareGenerationService
	}
}

func (p *Handler) Prepare(ctx context.Context, ev worker_client.EventWithOrgId) error {
	p.normalizeServices()
	kind := ev.Event.InvolvedObject.Kind
	name := ev.Event.InvolvedObject.Name
	identity, err := identityFromEvent(ev)
	if err != nil {
		return err
	}

	result, err := p.resolver.DeltaCandidates(ctx, ev)
	if err != nil {
		return err
	}
	if result.Skip {
		return p.finishSkip(ctx, ev, ev.OrgId, kind, name, identity)
	}

	prep, err := p.admitPrepare(ctx, ev.OrgId, kind, name, identity, result.Fleet)
	if err != nil {
		return err
	}
	if prep == nil {
		return nil
	}
	return p.processCandidates(ctx, ev, identity, prep, result)
}

func (p *Handler) processCandidates(ctx context.Context, ev worker_client.EventWithOrgId, identity prepareIdentity, prep *model.DeltaPrepare, result DeltaCandidateResult) error {
	kind := ev.Event.InvolvedObject.Kind
	name := ev.Event.InvolvedObject.Name

	// Admission is optimistic. The store transaction has already released its
	// row lock, so verify that this prepare is still the current waiting one
	// before doing any generation work.
	current, err := p.isCurrentPrepare(ctx, ev.OrgId, kind, name, prep, identity)
	if err != nil {
		return err
	}
	if !current {
		return nil
	}

	generationInputs := make([]*model.DeltaGeneration, 0, len(result.Candidates))
	for _, candidate := range result.Candidates {
		generationInputs = append(generationInputs, &model.DeltaGeneration{
			OrgID:           ev.OrgId,
			ImageRepository: candidate.ImageRepository,
			SourceDigest:    candidate.CurrentDigest,
			TargetDigest:    candidate.NewDigest,
		})
	}
	generations, err := p.generationService.CreateDeltaGenerations(ctx, generationInputs)
	if err != nil {
		return err
	}
	keys := generationKeysFromGenerations(generations)
	allTerminal, completed := generationProgress(generations)

	current, err = p.isCurrentPrepare(ctx, ev.OrgId, kind, name, prep, identity)
	if err != nil {
		return err
	}
	if !current {
		return nil
	}
	if err := p.createPrepareGenerations(ctx, prep.ID, keys); err != nil {
		return err
	}
	zeroWait := isZeroWait(p.maxWait(result.Fleet))
	current, err = p.isCurrentPrepare(ctx, ev.OrgId, kind, name, prep, identity)
	if err != nil {
		return err
	}
	if !current {
		return nil
	}
	if err := p.enqueuePending(ctx, ev.OrgId, result.Fleet, pendingGenerationKeys(generations)); err != nil {
		return err
	}
	if allTerminal || zeroWait {
		return p.completeNow(ctx, prep, ev.OrgId, kind, name)
	}
	return p.setPreparing(ctx, ev.OrgId, kind, name, completed, len(keys))
}

func (p *Handler) isCurrentPrepare(ctx context.Context, orgID uuid.UUID, kind, name string, prep *model.DeltaPrepare, identity prepareIdentity) (bool, error) {
	// This is an optimistic read, not a database lock. The admission
	// transaction has already committed and released its row lock; this check
	// only prevents stale work from continuing after a newer prepare replaces it.
	current, err := p.prepareService.GetDeltaPrepare(ctx, deltastore.PrepareKey{OrgID: orgID, Kind: kind, Name: name}, deltastore.WithPrepareStatus(model.DeltaPrepareWaiting))
	if err != nil {
		return false, err
	}
	if current == nil {
		return false, nil
	}
	return current.ID == prep.ID && current.ResourceVersion == prep.ResourceVersion && samePrepareIdentity(current, identity), nil
}

// CompleteWaitingIfTerminal completes prepares whose joined generations have
// all reached a terminal state. It is called after a generation update.
func (p *Handler) CompleteWaitingIfTerminal(ctx context.Context, key deltastore.GenerationKey) error {
	p.normalizeServices()
	if p.prepareGenerationService == nil || p.prepareService == nil {
		return fmt.Errorf("delta prepare services are required")
	}
	joins, err := p.prepareGenerationService.ListDeltaPrepareGenerations(ctx, deltastore.DeltaPrepareGenerationListFilter{GenerationKey: &key})
	if err != nil {
		return err
	}
	seen := make(map[uuid.UUID]struct{}, len(joins))
	for _, join := range joins {
		if _, ok := seen[join.PrepareID]; ok {
			continue
		}
		seen[join.PrepareID] = struct{}{}
		prep, err := p.prepareService.GetDeltaPrepare(ctx, deltastore.PrepareKey{ID: join.PrepareID})
		if err != nil {
			return err
		}
		if prep == nil || prep.Status != model.DeltaPrepareWaiting {
			continue
		}
		allJoins, err := p.prepareGenerationService.ListDeltaPrepareGenerations(ctx, deltastore.DeltaPrepareGenerationListFilter{PrepareID: &join.PrepareID})
		if err != nil {
			return err
		}
		keys := make([]deltastore.GenerationKey, 0, len(allJoins))
		for _, item := range allJoins {
			keys = append(keys, deltastore.GenerationKey{OrgID: item.OrgID, ImageRepository: item.ImageRepository, SourceDigest: item.SourceDigest, TargetDigest: item.TargetDigest})
		}
		allTerminal, _, _, err := p.completedCount(ctx, keys)
		if err != nil {
			return err
		}
		if allTerminal {
			if err := p.completeNow(ctx, prep, prep.OrgID, prep.Kind, prep.Name); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *Handler) completedCount(ctx context.Context, keys []deltastore.GenerationKey) (bool, int, map[deltastore.GenerationKey]bool, error) {
	if len(keys) == 0 {
		return true, 0, nil, nil
	}
	generations, err := p.generationService.ListDeltaGenerations(ctx, keys)
	if err != nil {
		return false, 0, nil, err
	}
	terminal := make(map[deltastore.GenerationKey]bool, len(generations))
	completed := 0
	for _, generation := range generations {
		if !isTerminalGeneration(generation.Status) {
			continue
		}
		completed++
		terminal[deltastore.GenerationKey{
			OrgID:           generation.OrgID,
			ImageRepository: generation.ImageRepository,
			SourceDigest:    generation.SourceDigest,
			TargetDigest:    generation.TargetDigest,
		}] = true
	}
	return completed == len(keys), completed, terminal, nil
}

func (p *Handler) admitPrepare(ctx context.Context, orgId uuid.UUID, kind, name string, identity prepareIdentity, fleet *domain.Fleet) (*model.DeltaPrepare, error) {
	now := p.now()
	prep := &model.DeltaPrepare{
		ID:                    uuid.New(),
		OrgID:                 orgId,
		Kind:                  kind,
		Name:                  name,
		TemplateVersion:       identity.templateVersion,
		SpecHash:              identity.specHash,
		SourceResourceVersion: identity.resourceVersion,
		CreatedAt:             now,
		Status:                model.DeltaPrepareWaiting,
	}
	if maxWait := p.maxWait(fleet); maxWait != nil {
		deadline := now.Add(*maxWait)
		prep.Deadline = &deadline
	}
	admission, err := p.prepareService.CreateOrReplaceWaitingDeltaPrepare(ctx, prep)
	if err != nil {
		return nil, err
	}
	if !admission.Accepted {
		return nil, nil
	}
	return admission.Prepare, nil
}

func (p *Handler) finishSkip(ctx context.Context, ev worker_client.EventWithOrgId, orgId uuid.UUID, kind, name string, identity prepareIdentity) error {
	latest, err := p.prepareService.GetDeltaPrepare(ctx, deltastore.PrepareKey{OrgID: orgId, Kind: kind, Name: name})
	if err != nil {
		return err
	}
	if latest == nil {
		return p.clearStatus(ctx, orgId, kind, name)
	}
	if latest.SourceResourceVersion > identity.resourceVersion {
		return nil
	}
	if latest.SourceResourceVersion == identity.resourceVersion {
		if !samePrepareIdentity(latest, identity) {
			return fmt.Errorf("conflicting delta prepares have source resource version %d", identity.resourceVersion)
		}
		if latest.Status != model.DeltaPrepareWaiting {
			return nil
		}
	}
	if latest.Status == model.DeltaPrepareWaiting {
		if err := p.failWaiting(ctx, latest, orgId, kind, name); err != nil {
			return err
		}
	}
	if err := p.clearStatus(ctx, orgId, kind, name); err != nil {
		return err
	}
	return p.emitResume(ctx, orgId, kind, name, &model.DeltaPrepare{TemplateVersion: identity.templateVersion})
}

func (p *Handler) failWaiting(ctx context.Context, waiting *model.DeltaPrepare, orgId uuid.UUID, kind, name string) error {
	waiting.Status = model.DeltaPrepareFailed
	_, err := p.prepareService.UpdateDeltaPrepare(ctx, waiting.ResourceVersion, waiting)
	if err != nil {
		if errors.Is(err, flterrors.ErrNoRowsUpdated) {
			return nil
		}
		return err
	}
	return p.clearStatus(ctx, orgId, kind, name)
}

func generationProgress(generations []model.DeltaGeneration) (allTerminal bool, completed int) {
	allTerminal = true
	for _, generation := range generations {
		if isTerminalGeneration(generation.Status) {
			completed++
			continue
		}
		allTerminal = false
	}
	return allTerminal, completed
}

func generationKeysFromGenerations(generations []model.DeltaGeneration) []deltastore.GenerationKey {
	keys := make([]deltastore.GenerationKey, 0, len(generations))
	for _, generation := range generations {
		keys = append(keys, deltastore.GenerationKey{
			OrgID:           generation.OrgID,
			ImageRepository: generation.ImageRepository,
			SourceDigest:    generation.SourceDigest,
			TargetDigest:    generation.TargetDigest,
		})
	}
	return keys
}

func pendingGenerationKeys(generations []model.DeltaGeneration) []deltastore.GenerationKey {
	keys := make([]deltastore.GenerationKey, 0, len(generations))
	for _, generation := range generations {
		if generation.Status != model.DeltaGenerationPending {
			continue
		}
		keys = append(keys, deltastore.GenerationKey{
			OrgID:           generation.OrgID,
			ImageRepository: generation.ImageRepository,
			SourceDigest:    generation.SourceDigest,
			TargetDigest:    generation.TargetDigest,
		})
	}
	return keys
}

func (p *Handler) enqueuePending(ctx context.Context, orgId uuid.UUID, fleet *domain.Fleet, pending []deltastore.GenerationKey) error {
	if len(pending) == 0 {
		return nil
	}
	if p.emit == nil {
		return fmt.Errorf("emit is required to enqueue generate jobs")
	}
	timeout := p.jobTimeout(fleet)
	for _, key := range pending {
		payload, err := json.Marshal(generateTask.GenerateDeltaPayload{
			ImageRepository: key.ImageRepository,
			SourceDigest:    key.SourceDigest,
			TargetDigest:    key.TargetDigest,
			Timeout:         durationPayload(timeout),
		})
		if err != nil {
			return err
		}
		if err := p.emit(ctx, orgId, &domain.Event{
			Reason:  domain.EventReasonGenerateDelta,
			Message: string(payload),
		}); err != nil {
			return err
		}
	}
	return nil
}

func (p *Handler) completeNow(ctx context.Context, prep *model.DeltaPrepare, orgId uuid.UUID, kind, name string) error {
	prep.Status = model.DeltaPrepareComplete
	updated, err := p.prepareService.UpdateDeltaPrepare(ctx, prep.ResourceVersion, prep)
	if err != nil {
		if errors.Is(err, flterrors.ErrNoRowsUpdated) {
			return nil
		}
		return err
	}
	if updated != nil {
		prep = updated
	}
	matches, err := p.identityMatches(ctx, prep)
	if err != nil {
		return err
	}
	if !matches {
		return nil
	}
	if err := p.clearStatus(ctx, orgId, kind, name); err != nil {
		return err
	}
	return p.emitResume(ctx, orgId, kind, name, prep)
}

func (p *Handler) identityMatches(ctx context.Context, prep *model.DeltaPrepare) (bool, error) {
	switch prep.Kind {
	case domain.FleetKind:
		if p.TVSvc == nil {
			return true, nil
		}
		tv, status := p.TVSvc.GetLatestTemplateVersion(ctx, prep.OrgID, prep.Name)
		if status.Code != http.StatusOK {
			if status.Code == http.StatusNotFound {
				return false, nil
			}
			return false, fmt.Errorf("getting latest template version for fleet %s: %s", prep.Name, status.Message)
		}
		if tv == nil {
			return false, nil
		}
		return equalStringPtr(prep.TemplateVersion, tv.Metadata.Name), nil
	case domain.DeviceKind:
		if p.DeviceSvc == nil {
			return true, nil
		}
		device, status := p.DeviceSvc.GetDevice(ctx, prep.OrgID, prep.Name)
		if status.Code != http.StatusOK {
			if status.Code == http.StatusNotFound {
				return false, nil
			}
			return false, fmt.Errorf("getting device %s: %s", prep.Name, status.Message)
		}
		if device == nil || device.Metadata.Generation == nil {
			return false, nil
		}
		return prep.SourceResourceVersion == *device.Metadata.Generation, nil
	default:
		return false, fmt.Errorf("unsupported prepare kind %q", prep.Kind)
	}
}

func (p *Handler) getFleet(ctx context.Context, orgId uuid.UUID, name string) (*domain.Fleet, error) {
	if p.FleetSvc == nil {
		return nil, fmt.Errorf("fleet service is required to resume fleet prepare")
	}
	fleet, status := p.FleetSvc.GetFleet(ctx, orgId, name, domain.GetFleetParams{})
	if status.Code != http.StatusOK {
		return nil, fmt.Errorf("getting fleet %s: %s", name, status.Message)
	}
	return fleet, nil
}

func (p *Handler) emitResume(ctx context.Context, orgId uuid.UUID, kind, name string, prep *model.DeltaPrepare) error {
	if p.Events == nil {
		return nil
	}
	switch kind {
	case domain.FleetKind:
		return p.emitFleetResume(ctx, orgId, name, prep)
	case domain.DeviceKind:
		if p.Events == nil {
			return fmt.Errorf("events service is required to resume device prepare")
		}
		p.Events.CreateEvent(ctx, orgId, domain.GetBaseEvent(ctx, domain.DeviceKind, name, domain.EventReasonDeltaGenerationCompleted, "Delta generation completed.", nil))
		return nil
	default:
		return fmt.Errorf("unsupported prepare kind %q", kind)
	}
}

func (p *Handler) emitFleetResume(ctx context.Context, orgId uuid.UUID, name string, prep *model.DeltaPrepare) error {
	if p.FleetSvc == nil || p.DeviceSvc == nil {
		return nil
	}
	fleet, err := p.getFleet(ctx, orgId, name)
	if err != nil {
		return err
	}
	tv := lo.FromPtr(prepTemplateVersion(prep))
	if tv == "" {
		tv = lo.FromPtr(liveFleetTemplateVersion(fleet, nil))
	}
	status := p.FleetSvc.UpdateFleetAnnotations(ctx, orgId, name, map[string]string{
		domain.FleetAnnotationTemplateVersion: tv,
	}, nil)
	if status.Code != http.StatusOK {
		return fmt.Errorf("setting fleet template version annotation: %s", status.Message)
	}
	if err := p.DeviceSvc.SetOutOfDate(ctx, orgId, util.ResourceOwner(domain.FleetKind, name)); err != nil {
		return err
	}
	immediate := fleet.Spec.RolloutPolicy == nil || fleet.Spec.RolloutPolicy.DeviceSelection == nil
	fleetservice.EmitFleetRolloutStartedEvent(ctx, p.Events, orgId, tv, name, immediate)
	return nil
}

func liveFleetTemplateVersion(fleet *domain.Fleet, fallback ...*string) *string {
	if fleet != nil && fleet.Metadata.Annotations != nil {
		if tv := (*fleet.Metadata.Annotations)[domain.FleetAnnotationTemplateVersion]; tv != "" {
			return &tv
		}
	}
	if len(fallback) > 0 {
		return fallback[0]
	}
	return nil
}

func prepTemplateVersion(prep *model.DeltaPrepare) *string {
	if prep == nil {
		return nil
	}
	return prep.TemplateVersion
}

func (p *Handler) createPrepareGenerations(ctx context.Context, prepareID uuid.UUID, keys []deltastore.GenerationKey) error {
	joins := make([]*model.DeltaPrepareGeneration, 0, len(keys))
	for _, key := range keys {
		joins = append(joins, &model.DeltaPrepareGeneration{
			PrepareID: prepareID, OrgID: key.OrgID, ImageRepository: key.ImageRepository,
			SourceDigest: key.SourceDigest, TargetDigest: key.TargetDigest,
		})
	}
	return p.prepareGenerationService.CreateDeltaPrepareGenerations(ctx, joins)
}

func (p *Handler) setPreparing(ctx context.Context, orgId uuid.UUID, kind, name string, completed, total int) error {
	if total == 0 {
		return nil
	}
	return p.prepareService.SetDeltaPreparingStatus(ctx, orgId, kind, name, completed, total)
}

func (p *Handler) clearStatus(ctx context.Context, orgId uuid.UUID, kind, name string) error {
	return p.prepareService.ClearDeltaPreparingStatus(ctx, orgId, kind, name)
}

func (p *Handler) now() time.Time {
	if p.Now == nil {
		return time.Now()
	}
	return p.Now()
}

func (p *Handler) maxWait(fleet *domain.Fleet) *time.Duration {
	maxWait, err := MaxWaitFromFleet(fleet, p.MaxWaitForDelta)
	if err != nil {
		return p.MaxWaitForDelta
	}
	return maxWait
}

func (p *Handler) jobTimeout(fleet *domain.Fleet) time.Duration {
	timeout, err := JobTimeoutFromFleet(fleet, p.DeltaGenerationTimeout)
	if err != nil {
		return p.DeltaGenerationTimeout
	}
	return timeout
}

func identityFromEvent(ev worker_client.EventWithOrgId) (prepareIdentity, error) {
	if ev.Event.Details == nil {
		return prepareIdentity{}, fmt.Errorf("prepare deltas event is missing details")
	}
	details, err := ev.Event.Details.AsPrepareDeltasDetails()
	if err != nil {
		return prepareIdentity{}, fmt.Errorf("prepare deltas details: %w", err)
	}
	if details.ResourceVersion == nil || *details.ResourceVersion == "" {
		return prepareIdentity{}, fmt.Errorf("prepare deltas event requires resourceVersion")
	}
	resourceVersion, err := strconv.ParseInt(*details.ResourceVersion, 10, 64)
	if err != nil || resourceVersion <= 0 {
		return prepareIdentity{}, fmt.Errorf("prepare deltas event has invalid resourceVersion %q", *details.ResourceVersion)
	}
	switch ev.Event.InvolvedObject.Kind {
	case domain.FleetKind:
		if details.TemplateVersion == nil || *details.TemplateVersion == "" {
			return prepareIdentity{}, fmt.Errorf("fleet prepare deltas event requires templateVersion")
		}
		return prepareIdentity{templateVersion: details.TemplateVersion, resourceVersion: resourceVersion}, nil
	case domain.DeviceKind:
		if details.SpecHash == nil || *details.SpecHash == "" {
			return prepareIdentity{}, fmt.Errorf("device prepare deltas event requires specHash")
		}
		return prepareIdentity{specHash: details.SpecHash, resourceVersion: resourceVersion}, nil
	default:
		return prepareIdentity{}, fmt.Errorf("unsupported involved object kind %q", ev.Event.InvolvedObject.Kind)
	}
}

func samePrepareIdentity(prep *model.DeltaPrepare, id prepareIdentity) bool {
	return equalStringPtr(prep.TemplateVersion, id.templateVersion) && equalStringPtr(prep.SpecHash, id.specHash) && prep.SourceResourceVersion == id.resourceVersion
}

func equalStringPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func isTerminalGeneration(status string) bool {
	return status == model.DeltaGenerationSucceeded || status == model.DeltaGenerationFailed || status == model.DeltaGenerationRejected
}

func isZeroWait(d *time.Duration) bool {
	return d != nil && *d == 0
}

func durationPayload(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	return d.String()
}

func MaxWaitFromFleet(fleet *domain.Fleet, deploy *time.Duration) (*time.Duration, error) {
	if fleet == nil || fleet.Spec.RolloutPolicy == nil || fleet.Spec.RolloutPolicy.DeltaGeneration == nil || fleet.Spec.RolloutPolicy.DeltaGeneration.MaxWaitForDelta == nil {
		return deploy, nil
	}
	d, err := time.ParseDuration(*fleet.Spec.RolloutPolicy.DeltaGeneration.MaxWaitForDelta)
	if err != nil {
		return nil, fmt.Errorf("rolloutPolicy.maxWaitForDelta: %w", err)
	}
	return &d, nil
}

func maxWaitFromFleet(fleet *domain.Fleet, deploy *time.Duration) (*time.Duration, error) {
	return MaxWaitFromFleet(fleet, deploy)
}

func JobTimeoutFromFleet(fleet *domain.Fleet, deploy time.Duration) (time.Duration, error) {
	if fleet == nil || fleet.Spec.RolloutPolicy == nil || fleet.Spec.RolloutPolicy.DeltaGeneration == nil || fleet.Spec.RolloutPolicy.DeltaGeneration.DeltaGenerationTimeout == nil {
		return deploy, nil
	}
	d, err := time.ParseDuration(*fleet.Spec.RolloutPolicy.DeltaGeneration.DeltaGenerationTimeout)
	if err != nil {
		return 0, fmt.Errorf("rolloutPolicy.deltaGenerationTimeout: %w", err)
	}
	return d, nil
}

func jobTimeoutFromFleet(fleet *domain.Fleet, deploy time.Duration) (time.Duration, error) {
	return JobTimeoutFromFleet(fleet, deploy)
}
