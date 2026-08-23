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

type generationJob struct {
	Key deltastore.GenerationKey
}

type generateDeltaPayload struct {
	ImageRepository string `json:"imageRepository"`
	SourceDigest    string `json:"sourceDigest"`
	TargetDigest    string `json:"targetDigest"`
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
	check    func(ctx context.Context, imageRepository, sourceDigest, targetDigest string) (existenceResult, error)
	generate func(ctx context.Context, sourceRef, targetRef, pushPath string) (deltaRef string, sizeBytes int64, err error)
	pushPath func(imageRepository string) (string, error)
	resume   func(ctx context.Context, key deltastore.GenerationKey) error
}

func parseGenerationJob(ev worker_client.EventWithOrgId) (generationJob, bool) {
	if ev.Event.Reason != domain.EventReasonGenerateDelta {
		return generationJob{}, false
	}
	if ev.OrgId == uuid.Nil {
		return generationJob{}, false
	}
	var payload generateDeltaPayload
	if err := json.Unmarshal([]byte(ev.Event.Message), &payload); err != nil {
		return generationJob{}, false
	}
	if payload.ImageRepository == "" || payload.SourceDigest == "" || payload.TargetDigest == "" {
		return generationJob{}, false
	}
	return generationJob{Key: deltastore.GenerationKey{
		OrgID:           ev.OrgId,
		ImageRepository: payload.ImageRepository,
		SourceDigest:    payload.SourceDigest,
		TargetDigest:    payload.TargetDigest,
	}}, true
}

func (p *pipeline) process(ctx context.Context, ev worker_client.EventWithOrgId, log logrus.FieldLogger) error {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	job, ok := parseGenerationJob(ev)
	if !ok {
		return nil
	}
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

	casErr := p.store.CASGeneration(ctx, key, claimed.ResourceVersion, deltastore.GenerationCAS{
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
	return p.runResume(ctx, key)
}

func (p *pipeline) failGeneration(ctx context.Context, key deltastore.GenerationKey, rv int64, cause error) error {
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
