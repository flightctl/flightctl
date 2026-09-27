package generate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/containers/image/v5/docker/reference"
	deltaconfig "github.com/flightctl/flightctl/internal/delta_worker/config"
	"github.com/flightctl/flightctl/internal/delta_worker/model"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltageneration"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltapreparegeneration"
	deltastore "github.com/flightctl/flightctl/internal/delta_worker/store/deltageneration"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/oci"
	repositoryservice "github.com/flightctl/flightctl/internal/service/repository"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/google/uuid"
	"github.com/opencontainers/go-digest"
	"github.com/sirupsen/logrus"
)

const (
	persistTimeout = 5 * time.Second
)

type generationJob struct {
	Key     deltastore.GenerationKey
	Timeout time.Duration
}

type GenerateDeltaPayload struct {
	ImageRepository string `json:"imageRepository"`
	SourceDigest    string `json:"sourceDigest"`
	TargetDigest    string `json:"targetDigest"`
	Timeout         string `json:"timeout,omitempty"`
}

type existingDelta struct {
	Ref       string
	SizeBytes int64
}

type existenceChecker func(ctx context.Context, orgID uuid.UUID, deltaRepository, sourceDigest, targetDigest string, spec *domain.OciRepoSpec) (*existingDelta, error)
type deltaGenerator func(ctx context.Context, generation *model.DeltaGeneration, spec *domain.OciRepoSpec, sourceRef, targetRef, pushPath string) (deltaRef string, sizeBytes int64, err error)

// EventEmitter publishes an internal delta-worker event and reports enqueue
// failures so the source generation task can be retried.
type EventEmitter func(context.Context, uuid.UUID, *domain.Event) error

type Handler struct {
	cfg            *deltaconfig.DeltaGenerationConfig
	log            logrus.FieldLogger
	repositories   repositoryservice.Service
	generations    deltageneration.Service
	progress       *deltapreparegeneration.ProgressHandler
	emit           EventEmitter
	kvStore        kvstore.KVStore
	existenceCheck existenceChecker
	generateDelta  deltaGenerator
}

func NewHandler(
	cfg *deltaconfig.DeltaGenerationConfig,
	log logrus.FieldLogger,
	repositories repositoryservice.Service,
	generations deltageneration.Service,
	progress *deltapreparegeneration.ProgressHandler,
	emit EventEmitter,
	kvStore kvstore.KVStore,
) (*Handler, error) {
	return newHandler(cfg, log, repositories, generations, nil, nil, progress, emit, kvStore)
}

func newHandler(
	cfg *deltaconfig.DeltaGenerationConfig,
	log logrus.FieldLogger,
	repositories repositoryservice.Service,
	generations deltageneration.Service,
	existenceCheck existenceChecker,
	generateDelta deltaGenerator,
	progress *deltapreparegeneration.ProgressHandler,
	emit EventEmitter,
	kvStore kvstore.KVStore,
) (*Handler, error) {
	if log == nil {
		return nil, fmt.Errorf("logger is required")
	}
	if repositories == nil {
		return nil, fmt.Errorf("repository service is required")
	}
	if generations == nil {
		return nil, fmt.Errorf("delta generation service is required")
	}
	if progress == nil {
		return nil, fmt.Errorf("delta generation progress handler is required")
	}
	if emit == nil {
		return nil, fmt.Errorf("event emitter is required")
	}
	h := &Handler{
		cfg:          cfg,
		log:          log,
		repositories: repositories,
		generations:  generations,
		progress:     progress,
		emit:         emit,
		kvStore:      kvStore,
	}
	if existenceCheck == nil {
		existenceCheck = h.defaultExistenceCheck
	}
	if generateDelta == nil {
		generateDelta = h.defaultGenerateDelta
	}
	h.existenceCheck = existenceCheck
	h.generateDelta = generateDelta
	return h, nil
}

func (c *Handler) Handle(ctx context.Context, ev worker_client.EventWithOrgId, log logrus.FieldLogger) error {
	job, ok, err := parseGenerationJob(ev)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	timeout := c.cfg.EffectiveTimeout()
	if job.Timeout > 0 {
		timeout = job.Timeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	key := job.Key
	log.Infof("generate delta repo=%s source=%s target=%s", key.ImageRepository, key.SourceDigest, key.TargetDigest)
	generation, err := c.generations.GetDeltaGeneration(ctx, key)
	if err != nil {
		return err
	}
	if generation.Status != model.DeltaGenerationPending {
		if isTerminalGenerationStatus(generation.Status) {
			if err := c.cacheSuccessfulGeneration(ctx, generation); err != nil {
				c.log.WithError(err).Warn("failed caching completed delta generation hint")
			}
			return c.emitGenerationComplete(ctx, generation)
		}
		log.Infof("did not claim %s generation for %s", generation.Status, key.ImageRepository)
		return nil
	}

	generation.Status = model.DeltaGenerationInProgress
	checkingPhase := string(domain.DeltaGenerationPhaseCheckingExisting)
	generation.Phase = &checkingPhase
	claimed, err := c.updateGeneration(ctx, generation)
	if errors.Is(err, flterrors.ErrNoRowsUpdated) {
		log.Infof("did not claim in_progress generation for %s", key.ImageRepository)
		return nil
	}
	if err != nil {
		return fmt.Errorf("claim generation: %w", err)
	}

	spec, err := ResolveDeltaTargetRepo(ctx, c.repositories, c.cfg, key.OrgID)
	if err != nil {
		return c.releaseGeneration(ctx, claimed, err)
	}

	pushPath := key.ImageRepository
	if spec != nil {
		pushPath, err = oci.ResolveDeltaPushPath(spec, key.ImageRepository)
		if err != nil {
			return c.releaseGeneration(ctx, claimed, err)
		}
	}

	existing, err := c.existenceCheck(ctx, key.OrgID, pushPath, key.SourceDigest, key.TargetDigest, spec)
	if err != nil {
		return c.releaseGeneration(ctx, claimed, err)
	}
	if existing != nil {
		log.Infof("delta already exists ref=%s sizeBytes=%d", existing.Ref, existing.SizeBytes)
		return c.completeGeneration(ctx, claimed, existing.Ref, existing.SizeBytes, log)
	}

	sourceRef := key.ImageRepository + "@" + key.SourceDigest
	targetRef := key.ImageRepository + "@" + key.TargetDigest
	log.Infof("creating delta source=%s target=%s push=%s", sourceRef, targetRef, pushPath)
	deltaRef, sizeBytes, err := c.generateDelta(ctx, claimed, spec, sourceRef, targetRef, pushPath)
	if err != nil {
		return c.failGeneration(ctx, claimed, err)
	}
	log.Infof("created delta %s sizeBytes=%d", deltaRef, sizeBytes)

	return c.completeGeneration(ctx, claimed, deltaRef, sizeBytes, log)
}

func ValidateGenerationJob(ev worker_client.EventWithOrgId) error {
	_, ok, err := parseGenerationJob(ev)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("invalid GenerateDelta payload")
	}
	return nil
}

func parseGenerationJob(ev worker_client.EventWithOrgId) (generationJob, bool, error) {
	if ev.Event.Reason != domain.EventReasonGenerateDelta {
		return generationJob{}, false, nil
	}
	var payload GenerateDeltaPayload
	if err := json.Unmarshal([]byte(ev.Event.Message), &payload); err != nil {
		return generationJob{}, false, nil
	}
	if payload.ImageRepository == "" || payload.SourceDigest == "" || payload.TargetDigest == "" {
		return generationJob{}, false, nil
	}
	if err := validateGenerationPayload(payload); err != nil {
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

func validateGenerationPayload(payload GenerateDeltaPayload) error {
	rewritten, err := oci.RewriteImageRef(payload.ImageRepository)
	if err != nil {
		return err
	}
	if _, err := reference.ParseNormalizedNamed(rewritten); err != nil {
		return err
	}
	if _, err := digest.Parse(payload.SourceDigest); err != nil {
		return err
	}
	if _, err := digest.Parse(payload.TargetDigest); err != nil {
		return err
	}
	return nil
}

func ResolveDeltaTargetRepo(ctx context.Context, repositoriesService repositoryservice.Service, cfg *deltaconfig.DeltaGenerationConfig, orgID uuid.UUID) (*domain.OciRepoSpec, error) {
	if repositoriesService == nil {
		return WriteSpecFromConfig(cfg), nil
	}
	limit := int32(1)
	fieldSelector := "spec.deltaStorageTarget=true"
	repositories, status := repositoriesService.ListRepositories(ctx, orgID, domain.ListRepositoriesParams{
		Limit:         &limit,
		FieldSelector: &fieldSelector,
	})
	if status.Code != http.StatusOK {
		return nil, fmt.Errorf("list delta storage target repositories: %s", status.Message)
	}
	if repositories != nil && len(repositories.Items) > 0 {
		spec, err := repositories.Items[0].Spec.AsOciRepoSpec()
		if err != nil {
			return nil, err
		}
		return &spec, nil
	}
	return WriteSpecFromConfig(cfg), nil
}

func (c *Handler) defaultExistenceCheck(ctx context.Context, _ uuid.UUID, deltaRepository, sourceDigest, targetDigest string, spec *domain.OciRepoSpec) (*existingDelta, error) {
	return checkExistingDelta(ctx, deltaRepository, sourceDigest, targetDigest, spec)
}

func (c *Handler) defaultGenerateDelta(ctx context.Context, generation *model.DeltaGeneration, spec *domain.OciRepoSpec, sourceRef, targetRef, pushPath string) (string, int64, error) {
	g := generator{run: execRunner{}, writeSpec: spec, log: c.log}
	g.phaseUpdate = func(phase domain.DeltaGenerationPhase) error {
		updated, err := c.updateGenerationPhase(ctx, generation, phase)
		if err == nil {
			*generation = *updated
		}
		return err
	}
	return g.createAndPushDelta(ctx, sourceRef, targetRef, pushPath)
}

func (c *Handler) updateGenerationPhase(ctx context.Context, generation *model.DeltaGeneration, phase domain.DeltaGenerationPhase) (*model.DeltaGeneration, error) {
	phaseValue := string(phase)
	generation.Phase = &phaseValue
	return c.updateGeneration(ctx, generation)
}

func (c *Handler) updateGeneration(ctx context.Context, generation *model.DeltaGeneration) (*model.DeltaGeneration, error) {
	updated, err := c.generations.UpdateDeltaGeneration(ctx, generation.ResourceVersion, generation)
	if err != nil {
		return nil, err
	}
	if err := c.progress.EmitForGeneration(ctx, updated); err != nil {
		return nil, err
	}
	return updated, nil
}

func WriteSpecFromConfig(cfg *deltaconfig.DeltaGenerationConfig) *domain.OciRepoSpec {
	if cfg == nil || cfg.DefaultRepository == nil {
		return nil
	}
	spec, err := cfg.DefaultRepository.OciRepoSpec()
	if err != nil {
		return nil
	}
	return spec
}

func (c *Handler) releaseGeneration(ctx context.Context, generation *model.DeltaGeneration, cause error) error {
	writeCtx, cancel := persistContext(ctx)
	defer cancel()
	generation.Status = model.DeltaGenerationPending
	generation.Phase = nil
	_, err := c.updateGeneration(writeCtx, generation)
	if errors.Is(err, flterrors.ErrNoRowsUpdated) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("generate: %w; release pending failed: %w", cause, err)
	}
	return cause
}

func (c *Handler) completeGeneration(ctx context.Context, generation *model.DeltaGeneration, deltaRef string, sizeBytes int64, log logrus.FieldLogger) error {
	writeCtx, cancel := persistContext(ctx)
	defer cancel()
	generation.Status = model.DeltaGenerationSucceeded
	generation.DeltaRef = &deltaRef
	generation.SizeBytes = &sizeBytes
	updated, err := c.updateGeneration(writeCtx, generation)
	if errors.Is(err, flterrors.ErrNoRowsUpdated) {
		log.Infof("stale resource_version; not completing %s", generation.ImageRepository)
		return nil
	}
	if err != nil {
		return c.failGeneration(ctx, generation, err)
	}
	if updated != nil {
		generation = updated
	}
	if err := c.cacheSuccessfulGeneration(writeCtx, generation); err != nil {
		c.log.WithError(err).Warn("failed caching completed delta generation hint")
	}
	return c.emitGenerationComplete(writeCtx, generation)
}

func (c *Handler) cacheSuccessfulGeneration(ctx context.Context, generation *model.DeltaGeneration) error {
	if c.kvStore == nil || generation == nil || generation.Status != model.DeltaGenerationSucceeded || generation.DeltaRef == nil || *generation.DeltaRef == "" {
		return nil
	}
	value, err := json.Marshal(kvstore.DeltaGenerationHint{
		DeltaRef:  *generation.DeltaRef,
		SizeBytes: generation.SizeBytes,
	})
	if err != nil {
		return fmt.Errorf("marshal completed delta generation hint: %w", err)
	}
	key := (&kvstore.DeltaGenerationHintKey{
		OrgID:           generation.OrgID,
		ImageRepository: generation.ImageRepository,
		SourceDigest:    generation.SourceDigest,
		TargetDigest:    generation.TargetDigest,
	}).ComposeKey()
	if err := c.kvStore.Set(ctx, key, value, kvstore.DeltaGenerationHintTTL); err != nil {
		return fmt.Errorf("store completed delta generation hint: %w", err)
	}
	return nil
}

func persistContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
}

func (c *Handler) failGeneration(ctx context.Context, generation *model.DeltaGeneration, cause error) error {
	fields := logrus.Fields{
		"imageRepository": generation.ImageRepository,
		"sourceDigest":    generation.SourceDigest,
		"targetDigest":    generation.TargetDigest,
	}
	if generation.Phase != nil {
		fields["phase"] = *generation.Phase
	}
	c.log.WithFields(fields).WithError(cause).Error("delta generation failed")

	writeCtx, cancel := persistContext(ctx)
	defer cancel()
	generation.Status = model.DeltaGenerationFailed
	updated, casErr := c.updateGeneration(writeCtx, generation)
	if casErr != nil && !errors.Is(casErr, flterrors.ErrNoRowsUpdated) {
		return fmt.Errorf("generate: %w; persist failed status: %w", cause, casErr)
	}
	if casErr == nil {
		if updated != nil {
			generation = updated
		}
		if err := c.emitGenerationComplete(writeCtx, generation); err != nil {
			return err
		}
	}
	return nil
}

func (c *Handler) emitGenerationComplete(ctx context.Context, generation *model.DeltaGeneration) error {
	event, err := deltageneration.NewGenerationCompleteEvent(generation)
	if err != nil {
		return err
	}
	if err := c.emit(ctx, generation.OrgID, event); err != nil {
		return fmt.Errorf("emit generation complete event: %w", err)
	}
	return nil
}

func isTerminalGenerationStatus(status string) bool {
	return status == model.DeltaGenerationSucceeded || status == model.DeltaGenerationFailed || status == model.DeltaGenerationRejected
}
