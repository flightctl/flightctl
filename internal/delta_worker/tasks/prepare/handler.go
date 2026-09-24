package prepare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltageneration"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltaprepare"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltapreparegeneration"
	deltagenerationstore "github.com/flightctl/flightctl/internal/delta_worker/store/deltageneration"
	deltapreparestore "github.com/flightctl/flightctl/internal/delta_worker/store/deltaprepare"
	deltapreparegenerationstore "github.com/flightctl/flightctl/internal/delta_worker/store/deltapreparegeneration"
	generateTask "github.com/flightctl/flightctl/internal/delta_worker/tasks/generate"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/google/uuid"
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
	if emit == nil {
		return nil, fmt.Errorf("event emitter is required")
	}
	return &Handler{
		resolver:                 resolver,
		emit:                     emit,
		Now:                      time.Now,
		prepareService:           prepareService,
		generationService:        generationService,
		prepareGenerationService: prepareGenerationService,
	}, nil
}

func (p *Handler) Prepare(ctx context.Context, ev worker_client.EventWithOrgId) error {
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
	if result.Superseded {
		return nil
	}
	if result.Skip {
		return p.finishSkip(ctx, ev.OrgId, kind, name, identity)
	}

	prep, err := p.admitPrepare(ctx, ev.OrgId, kind, name, identity, result.Fleet)
	if err != nil {
		return err
	}
	if prep == nil {
		return nil
	}
	if prep.Status == model.DeltaPrepareComplete {
		return p.emitPrepareCompletion(ctx, prep)
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
	completed := generationProgress(generations)

	current, err = p.isCurrentPrepare(ctx, ev.OrgId, kind, name, prep, identity)
	if err != nil {
		return err
	}
	if !current {
		return nil
	}
	created, err := p.createPrepareGenerations(ctx, prep.ID, keys)
	if err != nil {
		return err
	}
	updatedPrepare, ok := created.UpdatedPrepares[prep.ID]
	if !ok {
		return nil
	}
	if updatedPrepare.Status == model.DeltaPrepareComplete {
		return p.emitPrepareCompletion(ctx, &updatedPrepare)
	}
	if updatedPrepare.Status != model.DeltaPrepareWaiting {
		return nil
	}
	prep = &updatedPrepare
	zeroWait := isZeroWait(p.maxWait(result.Fleet))
	current, err = p.isCurrentPrepare(ctx, ev.OrgId, kind, name, prep, identity)
	if err != nil {
		return err
	}
	if !current {
		return nil
	}
	if zeroWait {
		if err := p.enqueuePending(ctx, ev.OrgId, result.Fleet, pendingGenerationKeys(generations)); err != nil {
			return err
		}
		return p.completeNow(ctx, prep)
	}
	// Persist the resource-side marker before publishing generation work. A
	// generation can complete immediately after it is enqueued; the completion
	// handler must then observe DeltaPreparing to apply progress or resume the
	// resource instead of leaving the marker behind after this task returns.
	if err := p.setPreparing(ctx, prep, completed, len(keys)); err != nil {
		return err
	}
	return p.enqueuePending(ctx, ev.OrgId, result.Fleet, pendingGenerationKeys(generations))
}

func (p *Handler) isCurrentPrepare(ctx context.Context, orgID uuid.UUID, kind, name string, prep *model.DeltaPrepare, identity prepareIdentity) (bool, error) {
	// This is an optimistic read, not a database lock. The admission
	// transaction has already committed and released its row lock; this check
	// only prevents stale work from continuing after a newer prepare replaces it.
	current, err := p.prepareService.GetLatestDeltaPrepareForResource(ctx, orgID, kind, name, deltapreparestore.WithPrepareStatus(model.DeltaPrepareWaiting))
	if err != nil {
		return false, err
	}
	if current == nil {
		return false, nil
	}
	return current.ID == prep.ID && current.ResourceVersion == prep.ResourceVersion && samePrepareIdentity(current, identity), nil
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
		if admission.Prepare != nil && admission.Prepare.Status == model.DeltaPrepareComplete &&
			admission.Prepare.SourceResourceVersion == identity.resourceVersion && samePrepareIdentity(admission.Prepare, identity) {
			return admission.Prepare, nil
		}
		return nil, nil
	}
	return admission.Prepare, nil
}

func (p *Handler) finishSkip(ctx context.Context, orgId uuid.UUID, kind, name string, identity prepareIdentity) error {
	latest, err := p.prepareService.GetLatestDeltaPrepareForResource(ctx, orgId, kind, name)
	if err != nil {
		return err
	}
	if latest == nil {
		completion := &model.DeltaPrepare{
			OrgID:                 orgId,
			Kind:                  kind,
			Name:                  name,
			TemplateVersion:       identity.templateVersion,
			SpecHash:              identity.specHash,
			SourceResourceVersion: identity.resourceVersion,
		}
		if err := p.prepareService.SetDeltaPreparingStatus(ctx, completion, 0, 0); err != nil {
			return fmt.Errorf("set skipped delta preparing status: %w", err)
		}
		return p.emitPrepareCompletion(ctx, completion)
	}
	if latest.SourceResourceVersion > identity.resourceVersion {
		return nil
	}
	if latest.SourceResourceVersion == identity.resourceVersion {
		if !samePrepareIdentity(latest, identity) {
			return fmt.Errorf("conflicting delta prepares have source resource version %d", identity.resourceVersion)
		}
	}
	if latest.Status == model.DeltaPrepareWaiting {
		if err := p.failWaiting(ctx, latest); err != nil {
			return err
		}
	}
	if latest.Status == model.DeltaPrepareComplete {
		return p.emitPrepareCompletion(ctx, latest)
	}
	completion := &model.DeltaPrepare{
		OrgID:                 orgId,
		Kind:                  kind,
		Name:                  name,
		TemplateVersion:       identity.templateVersion,
		SpecHash:              identity.specHash,
		SourceResourceVersion: identity.resourceVersion,
	}
	if latest.SourceResourceVersion < identity.resourceVersion {
		// A newer skip event can supersede a waiting prepare without going
		// through admission, leaving the resource marker keyed to the older
		// prepare. Re-establish the marker with the newer identity so the
		// completion handler can perform its normal conditional cleanup. The
		// status setter fences against a resource state newer than this event.
		if err := p.prepareService.SetDeltaPreparingStatus(ctx, completion, 0, 0); err != nil {
			return fmt.Errorf("rebind skipped delta preparing status: %w", err)
		}
	}
	return p.emitPrepareCompletion(ctx, completion)
}

func (p *Handler) failWaiting(ctx context.Context, waiting *model.DeltaPrepare) error {
	waiting.Status = model.DeltaPrepareFailed
	_, err := p.prepareService.UpdateDeltaPrepare(ctx, waiting.ResourceVersion, waiting)
	if err != nil {
		if errors.Is(err, flterrors.ErrNoRowsUpdated) {
			return nil
		}
		return err
	}
	return nil
}

func generationProgress(generations []model.DeltaGeneration) (completed int) {
	for _, generation := range generations {
		if isTerminalGeneration(generation.Status) {
			completed++
		}
	}
	return completed
}

func generationKeysFromGenerations(generations []model.DeltaGeneration) []deltagenerationstore.GenerationKey {
	keys := make([]deltagenerationstore.GenerationKey, 0, len(generations))
	for _, generation := range generations {
		keys = append(keys, deltagenerationstore.GenerationKey{
			OrgID:           generation.OrgID,
			ImageRepository: generation.ImageRepository,
			SourceDigest:    generation.SourceDigest,
			TargetDigest:    generation.TargetDigest,
		})
	}
	return keys
}

func pendingGenerationKeys(generations []model.DeltaGeneration) []deltagenerationstore.GenerationKey {
	keys := make([]deltagenerationstore.GenerationKey, 0, len(generations))
	for _, generation := range generations {
		if generation.Status != model.DeltaGenerationPending {
			continue
		}
		keys = append(keys, deltagenerationstore.GenerationKey{
			OrgID:           generation.OrgID,
			ImageRepository: generation.ImageRepository,
			SourceDigest:    generation.SourceDigest,
			TargetDigest:    generation.TargetDigest,
		})
	}
	return keys
}

func (p *Handler) enqueuePending(ctx context.Context, orgId uuid.UUID, fleet *domain.Fleet, pending []deltagenerationstore.GenerationKey) error {
	if len(pending) == 0 {
		return nil
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

func (p *Handler) completeNow(ctx context.Context, prep *model.DeltaPrepare) error {
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
	return p.emitPrepareCompletion(ctx, prep)
}

func (p *Handler) emitPrepareCompletion(ctx context.Context, prep *model.DeltaPrepare) error {
	event, err := deltaprepare.NewPrepareCompletionEvent(prep)
	if err != nil {
		return err
	}
	return p.emit(ctx, prep.OrgID, event)
}

func (p *Handler) createPrepareGenerations(ctx context.Context, prepareID uuid.UUID, keys []deltagenerationstore.GenerationKey) (deltapreparegenerationstore.CreateDeltaPrepareGenerationsResult, error) {
	joins := make([]*model.DeltaPrepareGeneration, 0, len(keys))
	for _, key := range keys {
		joins = append(joins, &model.DeltaPrepareGeneration{
			PrepareID: prepareID, OrgID: key.OrgID, ImageRepository: key.ImageRepository,
			SourceDigest: key.SourceDigest, TargetDigest: key.TargetDigest,
		})
	}
	return p.prepareGenerationService.CreateDeltaPrepareGenerations(ctx, joins)
}

func (p *Handler) setPreparing(ctx context.Context, prep *model.DeltaPrepare, completed, total int) error {
	if total == 0 {
		return nil
	}
	return p.prepareService.SetDeltaPreparingStatus(ctx, prep, completed, total)
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
