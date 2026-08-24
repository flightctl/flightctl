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
}

type pipeline struct {
	store    generationStore
	timeout  time.Duration
	check    func(ctx context.Context, imageRepository, sourceDigest, targetDigest string) (existenceResult, error)
	generate func(ctx context.Context, sourceRef, targetRef, pushPath string) (deltaRef string, sizeBytes int64, err error)
	pushPath func(imageRepository string) (string, error)
	prepare  func(ctx context.Context, ev worker_client.EventWithOrgId) error
	preparer *Preparer
}

func parseGenerationJob(ev worker_client.EventWithOrgId) (generationJob, bool, error) {
	if ev.Event.Reason != domain.EventReasonGenerateDelta {
		return generationJob{}, false, nil
	}
	if ev.OrgId == uuid.Nil {
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
		return p.prepare(ctx, ev)
	}

	job, ok, err := parseGenerationJob(ev)
	if err != nil {
		return err
	}
	if !ok {
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
	result, err := p.check(ctx, key.ImageRepository, key.SourceDigest, key.TargetDigest)
	if err != nil {
		return err
	}
	if result.Status == existenceInconclusive {
		log.Infof("existence check inconclusive for %s", key.ImageRepository)
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
		pushPath, err = p.pushPath(key.ImageRepository)
		if err != nil {
			return p.failGeneration(ctx, key, claimed.ResourceVersion, err)
		}
	}

	sourceRef := key.ImageRepository + "@" + key.SourceDigest
	targetRef := key.ImageRepository + "@" + key.TargetDigest
	deltaRef, sizeBytes, genErr := p.generate(ctx, sourceRef, targetRef, pushPath)
	if genErr != nil {
		return p.failGeneration(ctx, key, claimed.ResourceVersion, genErr)
	}

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
	if p.preparer == nil {
		return nil
	}
	return p.preparer.completeWaitingIfTerminal(ctx, key)
}
