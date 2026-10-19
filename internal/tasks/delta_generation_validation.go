package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	deltaconfig "github.com/flightctl/flightctl/internal/delta_worker/config"
	"github.com/flightctl/flightctl/internal/delta_worker/model"
	deltastore "github.com/flightctl/flightctl/internal/delta_worker/store/deltageneration"
	"github.com/flightctl/flightctl/internal/kvstore"
	repositoryservice "github.com/flightctl/flightctl/internal/service/repository"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

const deltaGenerationValidationCursorKey = "deltaGenerationValidation/cursor/"
const deltaGenerationValidationTimeBudget = time.Minute

type artifactAvailability int

const (
	artifactAvailable artifactAvailability = iota
	artifactMissing
	artifactAccessDenied
	artifactUnknown
)

type artifactAvailabilityChecker interface {
	Check(ctx context.Context, generation model.DeltaGeneration) (artifactAvailability, error)
}

type deltaGenerationValidationCursor struct {
	After *deltastore.GenerationKey `json:"after,omitempty"`
}

type DeltaGenerationValidator struct {
	log         logrus.FieldLogger
	generations deltastore.Store
	checker     artifactAvailabilityChecker
	kv          kvstore.KVStore
	batchSize   int
	timeBudget  time.Duration
	interval    time.Duration
}

func NewDeltaGenerationValidator(
	log logrus.FieldLogger,
	generations deltastore.Store,
	repositories repositoryservice.Service,
	kv kvstore.KVStore,
	cfg *deltaconfig.DeltaGenerationConfig,
	validationInterval time.Duration,
) (*DeltaGenerationValidator, error) {
	checker, err := newOCIDeltaArtifactChecker(cfg.EffectiveArtifactValidationTimeout(), repositories, cfg)
	if err != nil {
		return nil, err
	}
	return newDeltaGenerationValidator(log, generations, kv, checker, validationInterval)
}

func newDeltaGenerationValidator(
	log logrus.FieldLogger,
	generations deltastore.Store,
	kv kvstore.KVStore,
	checker artifactAvailabilityChecker,
	validationInterval time.Duration,
) (*DeltaGenerationValidator, error) {
	if log == nil {
		return nil, fmt.Errorf("delta generation validator logger is required")
	}
	if generations == nil {
		return nil, fmt.Errorf("delta generation service is required")
	}
	if checker == nil {
		return nil, fmt.Errorf("delta artifact availability checker is required")
	}
	if kv == nil {
		return nil, fmt.Errorf("delta generation validator KV store is required")
	}
	if validationInterval <= 0 {
		return nil, fmt.Errorf("delta generation validation interval must be greater than zero")
	}
	return &DeltaGenerationValidator{
		log:         log,
		generations: generations,
		checker:     checker,
		kv:          kv,
		batchSize:   500,
		timeBudget:  deltaGenerationValidationTimeBudget,
		interval:    validationInterval,
	}, nil
}

func (v *DeltaGenerationValidator) Poll(ctx context.Context) {
	now := time.Now()
	after, err := v.loadCursor(ctx)
	if err != nil {
		v.log.WithError(err).Error("Failed loading delta generation validation cursor")
		return
	}

	options := []deltastore.GenerationListOption{
		deltastore.WithListStatus(model.DeltaGenerationSucceeded),
		deltastore.WithListLastVerifiedBefore(now.Add(-v.interval)),
		deltastore.WithListLimit(v.batchSize),
	}
	if after != nil {
		options = append(options, deltastore.WithListAfter(*after))
	}
	generations, err := v.generations.ListDeltaGenerations(ctx, nil, options...)
	if err != nil {
		v.log.WithError(err).Error("Failed listing successful delta generations")
		return
	}
	if len(generations) == 0 {
		if after != nil {
			v.clearCursor(ctx)
		}
		return
	}

	deadline := time.Now().Add(v.timeBudget)
	for index, generation := range generations {
		v.pollGeneration(ctx, generation, now)
		if index < len(generations)-1 && time.Now().After(deadline) {
			after := deltaGenerationKey(generation)
			v.storeCursor(ctx, &after)
			return
		}
	}

	if len(generations) < v.batchSize {
		v.clearCursor(ctx)
		return
	}
	last := generations[len(generations)-1]
	next := deltaGenerationKey(last)
	v.storeCursor(ctx, &next)
}

func (v *DeltaGenerationValidator) pollGeneration(ctx context.Context, generation model.DeltaGeneration, now time.Time) {
	if generation.DeltaRef == nil || *generation.DeltaRef == "" {
		v.invalidateGeneration(ctx, generation, now, "Failed invalidating delta generation without an artifact reference")
		return
	}

	availability, err := v.checker.Check(ctx, generation)
	if err != nil {
		v.log.WithError(err).Warn("Failed checking delta artifact availability")
		return
	}
	if availability == artifactAvailable {
		verified := generation
		verified.LastVerifiedAt = lo.ToPtr(now)
		if _, err := v.generations.UpdateDeltaGeneration(ctx, generation.ResourceVersion, &verified); err != nil {
			v.log.WithError(err).Warn("Failed recording delta generation verification")
		}
		return
	}
	if availability != artifactMissing && availability != artifactAccessDenied {
		return
	}
	v.invalidateGeneration(ctx, generation, now, "Failed invalidating delta generation")
}

func (v *DeltaGenerationValidator) invalidateGeneration(ctx context.Context, generation model.DeltaGeneration, now time.Time, failureMessage string) {
	failed := generation
	failed.Status = model.DeltaGenerationFailed
	failed.DeltaRef = nil
	failed.SizeBytes = nil
	failed.Phase = nil
	failed.LastVerifiedAt = lo.ToPtr(now)

	updated, err := v.generations.UpdateDeltaGeneration(ctx, generation.ResourceVersion, &failed)
	if err != nil {
		v.log.WithError(err).Warn(failureMessage)
		return
	}
	if updated == nil {
		return
	}

	key := deltaGenerationKey(generation)
	if err := evictDeltaGenerationHints(ctx, v.kv, key); err != nil {
		v.log.WithError(err).Warn("Failed evicting delta generation hints")
	}
}

func (v *DeltaGenerationValidator) loadCursor(ctx context.Context) (*deltastore.GenerationKey, error) {
	raw, err := v.kv.Get(ctx, deltaGenerationValidationCursorKey)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var cursor deltaGenerationValidationCursor
	if err := json.Unmarshal(raw, &cursor); err != nil || cursor.After == nil {
		v.clearCursor(ctx)
		return nil, nil
	}
	return cursor.After, nil
}

func (v *DeltaGenerationValidator) storeCursor(ctx context.Context, after *deltastore.GenerationKey) {
	cursor := deltaGenerationValidationCursor{After: after}
	raw, err := json.Marshal(cursor)
	if err != nil {
		v.log.WithError(err).Error("Failed marshaling delta generation validation cursor")
		return
	}
	if err := v.kv.Set(ctx, deltaGenerationValidationCursorKey, raw, 0); err != nil {
		v.log.WithError(err).Error("Failed storing delta generation validation cursor")
	}
}

func (v *DeltaGenerationValidator) clearCursor(ctx context.Context) {
	if err := v.kv.Delete(ctx, deltaGenerationValidationCursorKey); err != nil {
		v.log.WithError(err).Error("Failed clearing delta generation validation cursor")
	}
}

func deltaGenerationKey(generation model.DeltaGeneration) deltastore.GenerationKey {
	return deltastore.GenerationKey{
		OrgID:           generation.OrgID,
		ImageRepository: generation.ImageRepository,
		SourceDigest:    generation.SourceDigest,
		TargetDigest:    generation.TargetDigest,
	}
}
