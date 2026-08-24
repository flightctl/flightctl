package delta_worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	deltastore "github.com/flightctl/flightctl/internal/store/delta"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/google/uuid"
)

type prepareStore interface {
	GetWaitingPrepare(ctx context.Context, orgID uuid.UUID, kind, name string) (*model.DeltaPrepare, error)
	InsertPrepare(ctx context.Context, prep *model.DeltaPrepare) error
	CASPrepareStatus(ctx context.Context, id uuid.UUID, to string) error
	InsertGenerations(ctx context.Context, gens []*model.DeltaGeneration) ([]deltastore.GenerationKey, error)
	InsertPrepareGenerations(ctx context.Context, prepareID uuid.UUID, keys []deltastore.GenerationKey) error
	GetGeneration(ctx context.Context, key deltastore.GenerationKey) (*model.DeltaGeneration, error)
}

type PreparingStatus interface {
	Set(ctx context.Context, orgId uuid.UUID, kind, name string, completed, total int) error
	Clear(ctx context.Context, orgId uuid.UUID, kind, name string) error
}

type Preparer struct {
	Resolver   *Resolver
	Store      prepareStore
	Emit       func(ctx context.Context, orgId uuid.UUID, event *domain.Event)
	Now        func() time.Time
	MaxWait    func(fleet *domain.Fleet) *time.Duration
	JobTimeout func(fleet *domain.Fleet) time.Duration
	Status     PreparingStatus
	Resume     func(ctx context.Context, ev worker_client.EventWithOrgId) error
}

type prepareIdentity struct {
	templateVersion *string
	resourceVersion *int64
}

func (p *Preparer) Prepare(ctx context.Context, ev worker_client.EventWithOrgId) error {
	if p.Resolver == nil {
		return fmt.Errorf("resolver is required")
	}
	if p.Store == nil {
		return fmt.Errorf("store is required")
	}

	kind := ev.Event.InvolvedObject.Kind
	name := ev.Event.InvolvedObject.Name
	identity, fleet, err := p.liveIdentity(ctx, ev)
	if err != nil {
		return err
	}
	if err := overlayLiveTemplateVersion(&ev, identity.templateVersion); err != nil {
		return err
	}

	result, err := p.Resolver.DeltaCandidates(ctx, ev)
	if err != nil {
		return err
	}
	if result.Skip {
		return p.finishSkip(ctx, ev, ev.OrgId, kind, name)
	}

	prep, err := p.waitingOrInsert(ctx, ev.OrgId, kind, name, identity, fleet)
	if err != nil {
		return err
	}
	if prep == nil {
		return nil
	}

	keys := candidateKeys(ev.OrgId, result.Candidates)
	allTerminal, completed, err := p.completedCount(ctx, keys)
	if err != nil {
		return err
	}

	changed, err := p.ensureGenerations(ctx, ev.OrgId, result.Candidates, allTerminal)
	if err != nil {
		return err
	}
	if !allTerminal {
		completed, err = p.countTerminal(ctx, keys)
		if err != nil {
			return err
		}
	}

	if err := p.Store.InsertPrepareGenerations(ctx, prep.ID, keys); err != nil {
		return err
	}

	zeroWait := isZeroWait(p.maxWait(fleet))
	if err := p.enqueueChanged(ctx, ev.OrgId, fleet, changed); err != nil {
		return err
	}
	if allTerminal || zeroWait {
		return p.completeNow(ctx, ev, prep, ev.OrgId, kind, name)
	}
	return p.setPreparing(ctx, ev.OrgId, kind, name, completed, len(keys))
}

func (p *Preparer) liveIdentity(ctx context.Context, ev worker_client.EventWithOrgId) (prepareIdentity, *domain.Fleet, error) {
	switch ev.Event.InvolvedObject.Kind {
	case domain.FleetKind:
		return p.fleetIdentity(ctx, ev)
	case domain.DeviceKind:
		id, err := p.deviceIdentity(ctx, ev)
		return id, nil, err
	default:
		return prepareIdentity{}, nil, fmt.Errorf("unsupported involved object kind %q", ev.Event.InvolvedObject.Kind)
	}
}

func (p *Preparer) fleetIdentity(ctx context.Context, ev worker_client.EventWithOrgId) (prepareIdentity, *domain.Fleet, error) {
	if p.Resolver.Fleet == nil {
		return prepareIdentity{}, nil, fmt.Errorf("fleet loader is required")
	}
	fleet, err := p.Resolver.Fleet(ctx, ev.OrgId, ev.Event.InvolvedObject.Name)
	if err != nil {
		return prepareIdentity{}, nil, err
	}
	return prepareIdentity{templateVersion: liveFleetTemplateVersion(fleet, eventTemplateVersion(ev))}, fleet, nil
}

func (p *Preparer) deviceIdentity(ctx context.Context, ev worker_client.EventWithOrgId) (prepareIdentity, error) {
	if p.Resolver.Device == nil {
		return prepareIdentity{}, fmt.Errorf("device loader is required")
	}
	device, err := p.Resolver.Device(ctx, ev.OrgId, ev.Event.InvolvedObject.Name)
	if err != nil {
		return prepareIdentity{}, err
	}
	return prepareIdentity{resourceVersion: device.Metadata.Generation}, nil
}

func (p *Preparer) waitingOrInsert(ctx context.Context, orgId uuid.UUID, kind, name string, identity prepareIdentity, fleet *domain.Fleet) (*model.DeltaPrepare, error) {
	waiting, err := p.Store.GetWaitingPrepare(ctx, orgId, kind, name)
	if err != nil {
		return nil, err
	}
	if waiting != nil && samePrepareIdentity(waiting, identity) {
		return waiting, nil
	}
	if waiting != nil {
		if err := p.failWaiting(ctx, waiting, orgId, kind, name); err != nil {
			return nil, err
		}
	}
	prep, err := p.insertWaiting(ctx, orgId, kind, name, identity, fleet)
	if err == nil {
		return prep, nil
	}
	if !errors.Is(err, flterrors.ErrDuplicateName) {
		return nil, err
	}
	existing, getErr := p.Store.GetWaitingPrepare(ctx, orgId, kind, name)
	if getErr != nil {
		return nil, getErr
	}
	if existing == nil || !samePrepareIdentity(existing, identity) {
		return nil, nil
	}
	return existing, nil
}

func (p *Preparer) finishSkip(ctx context.Context, ev worker_client.EventWithOrgId, orgId uuid.UUID, kind, name string) error {
	waiting, err := p.Store.GetWaitingPrepare(ctx, orgId, kind, name)
	if err != nil {
		return err
	}
	if waiting != nil {
		if err := p.failWaiting(ctx, waiting, orgId, kind, name); err != nil {
			return err
		}
	}
	if err := p.clearStatus(ctx, orgId, kind, name); err != nil {
		return err
	}
	return p.resume(ctx, ev)
}

func (p *Preparer) failWaiting(ctx context.Context, waiting *model.DeltaPrepare, orgId uuid.UUID, kind, name string) error {
	if err := p.Store.CASPrepareStatus(ctx, waiting.ID, model.DeltaPrepareFailed); err != nil && !errors.Is(err, flterrors.ErrNoRowsUpdated) {
		return err
	}
	return p.clearStatus(ctx, orgId, kind, name)
}

func (p *Preparer) insertWaiting(ctx context.Context, orgId uuid.UUID, kind, name string, identity prepareIdentity, fleet *domain.Fleet) (*model.DeltaPrepare, error) {
	now := p.now()
	prep := &model.DeltaPrepare{
		ID:                  uuid.New(),
		OrgID:               orgId,
		Kind:                kind,
		Name:                name,
		TemplateVersion:     identity.templateVersion,
		SpecResourceVersion: identity.resourceVersion,
		CreatedAt:           now,
		Status:              model.DeltaPrepareWaiting,
	}
	if maxWait := p.maxWait(fleet); maxWait != nil {
		deadline := now.Add(*maxWait)
		prep.Deadline = &deadline
	}
	if err := p.Store.InsertPrepare(ctx, prep); err != nil {
		return nil, err
	}
	return prep, nil
}

func (p *Preparer) ensureGenerations(ctx context.Context, orgId uuid.UUID, candidates []DeltaCandidate, allTerminal bool) ([]deltastore.GenerationKey, error) {
	if allTerminal {
		return nil, nil
	}
	gens := make([]*model.DeltaGeneration, 0, len(candidates))
	for _, c := range candidates {
		gens = append(gens, &model.DeltaGeneration{
			OrgID:           orgId,
			ImageRepository: c.ImageRepository,
			SourceDigest:    c.CurrentDigest,
			TargetDigest:    c.NewDigest,
		})
	}
	return p.Store.InsertGenerations(ctx, gens)
}

func (p *Preparer) completedCount(ctx context.Context, keys []deltastore.GenerationKey) (allTerminal bool, completed int, err error) {
	if len(keys) == 0 {
		return true, 0, nil
	}
	completed, err = p.countTerminal(ctx, keys)
	if err != nil {
		return false, 0, err
	}
	return completed == len(keys), completed, nil
}

func (p *Preparer) countTerminal(ctx context.Context, keys []deltastore.GenerationKey) (int, error) {
	completed := 0
	for _, key := range keys {
		gen, err := p.Store.GetGeneration(ctx, key)
		if errors.Is(err, flterrors.ErrResourceNotFound) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if isTerminalGeneration(gen.Status) {
			completed++
		}
	}
	return completed, nil
}

func (p *Preparer) enqueueChanged(ctx context.Context, orgId uuid.UUID, fleet *domain.Fleet, changed []deltastore.GenerationKey) error {
	if len(changed) == 0 {
		return nil
	}
	if p.Emit == nil {
		return fmt.Errorf("emit is required to enqueue generate jobs")
	}
	timeout := p.jobTimeout(fleet)
	for _, key := range changed {
		payload, err := json.Marshal(generateDeltaPayload{
			ImageRepository: key.ImageRepository,
			SourceDigest:    key.SourceDigest,
			TargetDigest:    key.TargetDigest,
			Timeout:         durationPayload(timeout),
		})
		if err != nil {
			return err
		}
		p.Emit(ctx, orgId, &domain.Event{
			Reason:  domain.EventReasonGenerateDelta,
			Message: string(payload),
		})
	}
	return nil
}

func (p *Preparer) completeNow(ctx context.Context, ev worker_client.EventWithOrgId, prep *model.DeltaPrepare, orgId uuid.UUID, kind, name string) error {
	if err := p.clearStatus(ctx, orgId, kind, name); err != nil {
		return err
	}
	if err := p.Store.CASPrepareStatus(ctx, prep.ID, model.DeltaPrepareComplete); err != nil {
		if errors.Is(err, flterrors.ErrNoRowsUpdated) {
			return nil
		}
		return err
	}
	return p.resume(ctx, ev)
}

func (p *Preparer) setPreparing(ctx context.Context, orgId uuid.UUID, kind, name string, completed, total int) error {
	if p.Status == nil || total == 0 {
		return nil
	}
	return p.Status.Set(ctx, orgId, kind, name, completed, total)
}

func (p *Preparer) clearStatus(ctx context.Context, orgId uuid.UUID, kind, name string) error {
	if p.Status == nil {
		return nil
	}
	return p.Status.Clear(ctx, orgId, kind, name)
}

func (p *Preparer) resume(ctx context.Context, ev worker_client.EventWithOrgId) error {
	if p.Resume == nil {
		return nil
	}
	return p.Resume(ctx, ev)
}

func (p *Preparer) now() time.Time {
	if p.Now == nil {
		return time.Now()
	}
	return p.Now()
}

func (p *Preparer) maxWait(fleet *domain.Fleet) *time.Duration {
	if p.MaxWait == nil {
		return nil
	}
	return p.MaxWait(fleet)
}

func (p *Preparer) jobTimeout(fleet *domain.Fleet) time.Duration {
	if p.JobTimeout == nil {
		return 0
	}
	return p.JobTimeout(fleet)
}

func overlayLiveTemplateVersion(ev *worker_client.EventWithOrgId, tv *string) error {
	if tv == nil {
		return nil
	}
	details := domain.PrepareDeltasDetails{
		DetailType:      domain.PrepareDeltasDetailsDetailType("PrepareDeltas"),
		TemplateVersion: tv,
	}
	if ev.Event.Details != nil {
		existing, err := ev.Event.Details.AsPrepareDeltasDetails()
		if err == nil {
			existing.TemplateVersion = tv
			details = existing
		}
	}
	var eventDetails domain.EventDetails
	if err := eventDetails.FromPrepareDeltasDetails(details); err != nil {
		return err
	}
	ev.Event.Details = &eventDetails
	return nil
}

func liveFleetTemplateVersion(fleet *domain.Fleet, fallback *string) *string {
	if fleet == nil || fleet.Metadata.Annotations == nil {
		return fallback
	}
	tv, ok := (*fleet.Metadata.Annotations)[domain.FleetAnnotationTemplateVersion]
	if !ok || tv == "" {
		return fallback
	}
	return &tv
}

func eventTemplateVersion(ev worker_client.EventWithOrgId) *string {
	if ev.Event.Details == nil {
		return nil
	}
	details, err := ev.Event.Details.AsPrepareDeltasDetails()
	if err != nil {
		return nil
	}
	return details.TemplateVersion
}

func samePrepareIdentity(prep *model.DeltaPrepare, id prepareIdentity) bool {
	return equalStringPtr(prep.TemplateVersion, id.templateVersion) && equalInt64Ptr(prep.SpecResourceVersion, id.resourceVersion)
}

func equalStringPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func equalInt64Ptr(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func candidateKeys(orgId uuid.UUID, candidates []DeltaCandidate) []deltastore.GenerationKey {
	keys := make([]deltastore.GenerationKey, 0, len(candidates))
	seen := make(map[deltastore.GenerationKey]struct{}, len(candidates))
	for _, c := range candidates {
		key := deltastore.GenerationKey{
			OrgID:           orgId,
			ImageRepository: c.ImageRepository,
			SourceDigest:    c.CurrentDigest,
			TargetDigest:    c.NewDigest,
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	return keys
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

func maxWaitFromFleet(fleet *domain.Fleet, deploy *time.Duration) (*time.Duration, error) {
	if fleet == nil || fleet.Spec.RolloutPolicy == nil || fleet.Spec.RolloutPolicy.MaxWaitForDelta == nil {
		return deploy, nil
	}
	d, err := time.ParseDuration(*fleet.Spec.RolloutPolicy.MaxWaitForDelta)
	if err != nil {
		return nil, fmt.Errorf("rolloutPolicy.maxWaitForDelta: %w", err)
	}
	return &d, nil
}

func jobTimeoutFromFleet(fleet *domain.Fleet, deploy time.Duration) (time.Duration, error) {
	if fleet == nil || fleet.Spec.RolloutPolicy == nil || fleet.Spec.RolloutPolicy.DeltaGenerationTimeout == nil {
		return deploy, nil
	}
	d, err := time.ParseDuration(*fleet.Spec.RolloutPolicy.DeltaGenerationTimeout)
	if err != nil {
		return 0, fmt.Errorf("rolloutPolicy.deltaGenerationTimeout: %w", err)
	}
	return d, nil
}
