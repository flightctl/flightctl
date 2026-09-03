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
	"github.com/sirupsen/logrus"
)

const persistTimeout = 5 * time.Second

type generationJob struct {
	Key     deltastore.GenerationKey
	Timeout time.Duration
}

type generateDeltaPayload struct {
	ImageRepository string `json:"imageRepository"`
	SourceDigest    string `json:"sourceDigest"`
	TargetDigest    string `json:"targetDigest"`
	Timeout         string `json:"timeout,omitempty"`
}

type generationStore interface {
	InsertRejectedGeneration(ctx context.Context, gen *model.DeltaGeneration) error
	InsertGenerations(ctx context.Context, gens []*model.DeltaGeneration) ([]deltastore.GenerationKey, error)
	ClaimGeneration(ctx context.Context, key deltastore.GenerationKey) (*model.DeltaGeneration, error)
	CASGeneration(ctx context.Context, key deltastore.GenerationKey, expectedRV int64, update deltastore.GenerationCAS) error
	ListWaitingPreparesByGeneration(ctx context.Context, key deltastore.GenerationKey) ([]model.DeltaPrepare, error)
	CountPreparePairs(ctx context.Context, prepareID uuid.UUID) (completed, total int, err error)
	SetGenerationPhase(ctx context.Context, key deltastore.GenerationKey, phase string) error
}

type pipeline struct {
	store    generationStore
	timeout  time.Duration
	check    func(ctx context.Context, orgID uuid.UUID, imageRepository, sourceDigest, targetDigest string) (existenceResult, error)
	generate func(ctx context.Context, orgID uuid.UUID, sourceRef, targetRef, pushPath string) (deltaRef string, sizeBytes int64, err error)
	pushPath func(ctx context.Context, orgID uuid.UUID, imageRepository string) (string, error)
	prepare  func(ctx context.Context, ev worker_client.EventWithOrgId) error
	status   PreparingStatus
	persist  func(ctx context.Context, orgId uuid.UUID, event *domain.Event)
	preparer *Preparer
}

func parseGenerationJob(ev worker_client.EventWithOrgId) (generationJob, bool, error) {
	if ev.Event.Reason != domain.EventReasonGenerateDelta {
		return generationJob{}, false, nil
	}
	var payload generateDeltaPayload
	if err := json.Unmarshal([]byte(ev.Event.Message), &payload); err != nil {
		return generationJob{}, false, nil
	}
	if payload.ImageRepository == "" || payload.SourceDigest == "" || payload.TargetDigest == "" {
		return generationJob{}, false, nil
	}
	job := generationJob{Key: deltastore.GenerationKey{
		OrgID:           ev.OrgId,
		ImageRepository: payload.ImageRepository,
		SourceDigest:    payload.SourceDigest,
		TargetDigest:    payload.TargetDigest,
	}}
	if payload.Timeout == "" {
		return job, true, nil
	}
	d, err := time.ParseDuration(payload.Timeout)
	if err != nil {
		return generationJob{}, false, fmt.Errorf("generate delta timeout: %w", err)
	}
	job.Timeout = d
	return job, true, nil
}

func (p *pipeline) process(ctx context.Context, ev worker_client.EventWithOrgId, log logrus.FieldLogger) error {
	if ev.Event.Reason == domain.EventReasonPrepareDeltas {
		if p.prepare == nil {
			return nil
		}
		ctx, cancel := context.WithTimeout(ctx, p.timeout)
		defer cancel()
		log.Infof("preparing deltas for %s/%s", ev.Event.InvolvedObject.Kind, ev.Event.InvolvedObject.Name)
		return p.prepare(ctx, ev)
	}

	job, ok, err := parseGenerationJob(ev)
	if err != nil {
		return err
	}
	if !ok {
		if ev.Event.Reason == domain.EventReasonGenerateDelta {
			log.Warnf("ignoring GenerateDelta: payload not parseable org=%s message=%q", ev.OrgId, ev.Event.Message)
		}
		return nil
	}
	timeout := p.timeout
	if job.Timeout > 0 {
		timeout = job.Timeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if p.store == nil {
		return nil
	}

	key := job.Key
	log.Infof("generate delta repo=%s source=%s target=%s", key.ImageRepository, key.SourceDigest, key.TargetDigest)
	checkPhase := domain.DeltaGenerationPhaseCheckingExisting
	p.fanoutProgress(ctx, key, domain.DeltaGenerationProgressInProgress, &checkPhase, log)
	result, err := p.check(ctx, key.OrgID, key.ImageRepository, key.SourceDigest, key.TargetDigest)
	if err != nil {
		return err
	}
	log.Infof("existence check repo=%s status=%s", key.ImageRepository, existenceStatusName(result.Status))
	if result.Status == existenceInconclusive {
		return nil
	}
	if result.Status == existenceFound {
		size := result.SizeBytes
		if err := p.store.InsertRejectedGeneration(ctx, &model.DeltaGeneration{
			OrgID:           key.OrgID,
			ImageRepository: key.ImageRepository,
			SourceDigest:    key.SourceDigest,
			TargetDigest:    key.TargetDigest,
			Status:          model.DeltaGenerationRejected,
			SizeBytes:       &size,
		}); err != nil {
			return err
		}
		p.completePair(ctx, key, domain.DeltaGenerationProgressRejected, log)
		return p.runResume(ctx, key)
	}

	if _, err := p.store.InsertGenerations(ctx, []*model.DeltaGeneration{{
		OrgID:           key.OrgID,
		ImageRepository: key.ImageRepository,
		SourceDigest:    key.SourceDigest,
		TargetDigest:    key.TargetDigest,
	}}); err != nil {
		return err
	}

	claimed, err := p.store.ClaimGeneration(ctx, key)
	if err != nil {
		if errors.Is(err, flterrors.ErrNoRowsUpdated) {
			log.Infof("did not claim in_progress generation for %s", key.ImageRepository)
			return nil
		}
		return err
	}

	pushPath := key.ImageRepository
	if p.pushPath != nil {
		pushPath, err = p.pushPath(ctx, key.OrgID, key.ImageRepository)
		if err != nil {
			return p.failGeneration(ctx, key, claimed.ResourceVersion, err)
		}
	}

	sourceRef := key.ImageRepository + "@" + key.SourceDigest
	targetRef := key.ImageRepository + "@" + key.TargetDigest
	log.Infof("creating delta source=%s target=%s push=%s", sourceRef, targetRef, pushPath)
	lastPhase := domain.DeltaGenerationPhaseCheckingExisting
	genCtx := withCopyProgress(ctx, func(prog GenerationProgress) {
		if prog.Phase == "" || prog.Phase == lastPhase {
			return
		}
		ph := prog.Phase
		lastPhase = ph
		p.fanoutProgress(ctx, key, domain.DeltaGenerationProgressInProgress, &ph, log)
	})
	deltaRef, sizeBytes, genErr := p.generate(genCtx, key.OrgID, sourceRef, targetRef, pushPath)
	if genErr != nil {
		return p.failGeneration(ctx, key, claimed.ResourceVersion, genErr)
	}
	log.Infof("created delta %s sizeBytes=%d", deltaRef, sizeBytes)

	writeCtx, writeCancel := persistContext(ctx)
	defer writeCancel()
	casErr := p.store.CASGeneration(writeCtx, key, claimed.ResourceVersion, deltastore.GenerationCAS{
		Status:    model.DeltaGenerationSucceeded,
		DeltaRef:  &deltaRef,
		SizeBytes: &sizeBytes,
	})
	if casErr != nil {
		if errors.Is(casErr, flterrors.ErrNoRowsUpdated) {
			log.Infof("stale resource_version; not completing %s", key.ImageRepository)
			return nil
		}
		return p.failGeneration(ctx, key, claimed.ResourceVersion, casErr)
	}
	p.completePair(writeCtx, key, domain.DeltaGenerationProgressSucceeded, log)
	return p.runResume(writeCtx, key)
}

func (p *pipeline) completePair(ctx context.Context, key deltastore.GenerationKey, status domain.DeltaGenerationProgressDetailsGenerationStatus, log logrus.FieldLogger) {
	p.fanoutProgress(ctx, key, status, nil, log)
	p.refreshPairCounts(ctx, key, log)
}

func (p *pipeline) fanoutProgress(ctx context.Context, key deltastore.GenerationKey, status domain.DeltaGenerationProgressDetailsGenerationStatus, phase *domain.DeltaGenerationPhase, log logrus.FieldLogger) {
	if p.persist == nil || p.store == nil {
		return
	}
	waiting, err := p.store.ListWaitingPreparesByGeneration(ctx, key)
	if err != nil {
		if log != nil {
			log.WithError(err).Warn("failed to list prepares for delta generation progress")
		}
		return
	}
	for i := range waiting {
		prep := waiting[i]
		event, err := deltaGenerationProgressEvent(ctx, prep, key, status, phase)
		if err != nil {
			if log != nil {
				log.WithError(err).Warnf("failed to build delta generation progress for %s/%s", prep.Kind, prep.Name)
			}
			continue
		}
		p.persist(ctx, prep.OrgID, event)
	}
	if status == domain.DeltaGenerationProgressInProgress && phase != nil && *phase != "" {
		if err := p.store.SetGenerationPhase(ctx, key, string(*phase)); err != nil && log != nil {
			log.WithError(err).Warnf("failed to persist delta generation phase for %s", key.ImageRepository)
		}
	}
}

func (p *pipeline) refreshPairCounts(ctx context.Context, key deltastore.GenerationKey, log logrus.FieldLogger) {
	if p.status == nil || p.store == nil {
		return
	}
	waiting, err := p.store.ListWaitingPreparesByGeneration(ctx, key)
	if err != nil {
		if log != nil {
			log.WithError(err).Warn("failed to list prepares for pair counts")
		}
		return
	}
	for i := range waiting {
		prep := waiting[i]
		completed, total, err := p.store.CountPreparePairs(ctx, prep.ID)
		if err != nil {
			if log != nil {
				log.WithError(err).Warnf("failed to count pairs for %s/%s", prep.Kind, prep.Name)
			}
			continue
		}
		if total == 0 {
			continue
		}
		if err := p.status.Set(ctx, prep.OrgID, prep.Kind, prep.Name, completed, total); err != nil && log != nil {
			log.WithError(err).Warnf("failed to update pair counts for %s/%s", prep.Kind, prep.Name)
		}
	}
}

func persistContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
}

func (p *pipeline) failGeneration(ctx context.Context, key deltastore.GenerationKey, rv int64, cause error) error {
	ctx, cancel := persistContext(ctx)
	defer cancel()
	casErr := p.store.CASGeneration(ctx, key, rv, deltastore.GenerationCAS{Status: model.DeltaGenerationFailed})
	if casErr != nil && !errors.Is(casErr, flterrors.ErrNoRowsUpdated) {
		return fmt.Errorf("generate: %w; persist failed status: %v", cause, casErr)
	}
	if casErr == nil {
		p.completePair(ctx, key, domain.DeltaGenerationProgressFailed, nil)
	}
	return p.runResume(ctx, key)
}

func (p *pipeline) runResume(ctx context.Context, key deltastore.GenerationKey) error {
	if p.preparer == nil {
		return nil
	}
	return p.preparer.CompleteWaitingIfTerminal(ctx, key)
}
