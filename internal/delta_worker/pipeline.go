package delta_worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	deltastore "github.com/flightctl/flightctl/internal/store/delta"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/worker_client"
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
}

type pipeline struct {
	store    generationStore
	timeout  time.Duration
	check    func(ctx context.Context, orgID uuid.UUID, imageRepository, sourceDigest, targetDigest string) (existenceResult, error)
	generate func(ctx context.Context, orgID uuid.UUID, sourceRef, targetRef, pushPath string) (deltaRef string, sizeBytes int64, err error)
	pushPath func(ctx context.Context, orgID uuid.UUID, imageRepository string) (string, error)
	resume   func(ctx context.Context, key deltastore.GenerationKey) error
	prepare  func(ctx context.Context, ev worker_client.EventWithOrgId) error
	status   PreparingStatus
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
	stopCheck := p.heartbeatPrepareProgress(ctx, key, domain.DeltaGenerationPhaseCheckingExisting, log)
	result, err := p.check(ctx, key.OrgID, key.ImageRepository, key.SourceDigest, key.TargetDigest)
	stopCheck()
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
	var lastMu sync.Mutex
	var last GenerationProgress
	genCtx := withCopyProgress(ctx, func(prog GenerationProgress) {
		lastMu.Lock()
		last = prog
		lastMu.Unlock()
		p.reportCopyProgress(ctx, key, prog, log)
	})
	stopGen := p.heartbeatLastProgress(ctx, key, &lastMu, &last, log)
	defer stopGen()
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
	return p.runResume(writeCtx, key)
}

func (p *pipeline) reportCopyProgress(ctx context.Context, key deltastore.GenerationKey, progress GenerationProgress, log logrus.FieldLogger) {
	if p.status == nil || p.store == nil {
		return
	}
	waiting, err := p.store.ListWaitingPreparesByGeneration(ctx, key)
	if err != nil {
		if log != nil {
			log.WithError(err).Warn("failed to list prepares for copy progress")
		}
		return
	}
	for i := range waiting {
		prep := waiting[i]
		if err := p.status.SetProgress(ctx, prep.OrgID, prep.Kind, prep.Name, progress); err != nil && log != nil {
			log.WithError(err).Warnf("failed to update copy progress for %s/%s", prep.Kind, prep.Name)
		}
	}
}

func (p *pipeline) heartbeatPrepareProgress(ctx context.Context, key deltastore.GenerationKey, phase domain.DeltaGenerationPhase, log logrus.FieldLogger) context.CancelFunc {
	progress := GenerationProgress{Phase: phase}
	p.reportCopyProgress(ctx, key, progress, log)
	return p.tickProgress(ctx, func() {
		p.reportCopyProgress(ctx, key, progress, log)
	})
}

func (p *pipeline) heartbeatLastProgress(ctx context.Context, key deltastore.GenerationKey, mu *sync.Mutex, last *GenerationProgress, log logrus.FieldLogger) context.CancelFunc {
	return p.tickProgress(ctx, func() {
		mu.Lock()
		cur := *last
		mu.Unlock()
		if cur.Phase == "" {
			return
		}
		p.reportCopyProgress(ctx, key, cur, log)
	})
}

func (p *pipeline) tickProgress(ctx context.Context, fn func()) context.CancelFunc {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		t := time.NewTicker(copyProgressInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				fn()
			}
		}
	}()
	return cancel
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
	return p.runResume(ctx, key)
}

func (p *pipeline) runResume(ctx context.Context, key deltastore.GenerationKey) error {
	if p.resume != nil {
		return p.resume(ctx, key)
	}
	return tryLastPairResume(ctx, p.store, key)
}

func tryLastPairResume(ctx context.Context, store generationStore, key deltastore.GenerationKey) error {
	_, err := store.ListWaitingPreparesByGeneration(ctx, key)
	return err
}
