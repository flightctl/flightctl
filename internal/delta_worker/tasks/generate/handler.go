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
	deltastore "github.com/flightctl/flightctl/internal/delta_worker/store"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
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

type Handler struct {
	cfg             *deltaconfig.DeltaGenerationConfig
	log             logrus.FieldLogger
	repositories    repositoryservice.Service
	generations     deltageneration.Service
	existenceCheck  existenceChecker
	generateDelta   deltaGenerator
	completePrepare func(context.Context, deltastore.GenerationKey) error
}

func NewHandler(
	cfg *deltaconfig.DeltaGenerationConfig,
	log logrus.FieldLogger,
	repositories repositoryservice.Service,
	generations deltageneration.Service,
) (*Handler, error) {
	return newHandler(cfg, log, repositories, generations, nil, nil)
}

// SetCompletePrepare registers the callback used to release a waiting prepare
// after a generation reaches a terminal state.
func (c *Handler) SetCompletePrepare(complete func(context.Context, deltastore.GenerationKey) error) {
	c.completePrepare = complete
}

func newHandler(
	cfg *deltaconfig.DeltaGenerationConfig,
	log logrus.FieldLogger,
	repositories repositoryservice.Service,
	generations deltageneration.Service,
	existenceCheck existenceChecker,
	generateDelta deltaGenerator,
) (*Handler, error) {
	if repositories == nil {
		return nil, fmt.Errorf("repository service is required")
	}
	if generations == nil {
		return nil, fmt.Errorf("delta generation service is required")
	}
	h := &Handler{
		cfg:          cfg,
		log:          log,
		repositories: repositories,
		generations:  generations,
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
		log.Infof("did not claim %s generation for %s", generation.Status, key.ImageRepository)
		return nil
	}

	generation.Status = model.DeltaGenerationInProgress
	checkingPhase := string(domain.DeltaGenerationPhaseCheckingExisting)
	generation.Phase = &checkingPhase
	claimed, err := c.generations.UpdateDeltaGeneration(ctx, generation.ResourceVersion, generation)
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
	return c.generations.UpdateDeltaGeneration(ctx, generation.ResourceVersion, generation)
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
	_, err := c.generations.UpdateDeltaGeneration(writeCtx, generation.ResourceVersion, generation)
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
	_, err := c.generations.UpdateDeltaGeneration(writeCtx, generation.ResourceVersion, generation)
	if errors.Is(err, flterrors.ErrNoRowsUpdated) {
		log.Infof("stale resource_version; not completing %s", generation.ImageRepository)
		return nil
	}
	if err != nil {
		return c.failGeneration(ctx, generation, err)
	}
	if c.completePrepare != nil {
		return c.completePrepare(writeCtx, keyForGeneration(generation))
	}
	return nil
}

func persistContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
}

func (c *Handler) failGeneration(ctx context.Context, generation *model.DeltaGeneration, cause error) error {
	writeCtx, cancel := persistContext(ctx)
	defer cancel()
	generation.Status = model.DeltaGenerationFailed
	_, casErr := c.generations.UpdateDeltaGeneration(writeCtx, generation.ResourceVersion, generation)
	if casErr != nil && !errors.Is(casErr, flterrors.ErrNoRowsUpdated) {
		return fmt.Errorf("generate: %w; persist failed status: %w", cause, casErr)
	}
	if casErr == nil && c.completePrepare != nil {
		return c.completePrepare(writeCtx, keyForGeneration(generation))
	}
	return nil
}

func keyForGeneration(generation *model.DeltaGeneration) deltastore.GenerationKey {
	return deltastore.GenerationKey{
		OrgID:           generation.OrgID,
		ImageRepository: generation.ImageRepository,
		SourceDigest:    generation.SourceDigest,
		TargetDigest:    generation.TargetDigest,
	}
}
