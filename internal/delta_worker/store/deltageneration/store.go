package deltageneration

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const generationListBatchSize = 500

// Store is the resource-oriented persistence API used by the
// generation service. Updates replace the complete object under CAS.
type Store interface {
	InsertDeltaGenerations(ctx context.Context, generations []*model.DeltaGeneration) ([]model.DeltaGeneration, error)
	GetDeltaGeneration(ctx context.Context, key GenerationKey, opts ...GenerationGetOption) (*model.DeltaGeneration, error)
	ListDeltaGenerations(ctx context.Context, keys []GenerationKey) ([]model.DeltaGeneration, error)
	UpdateDeltaGeneration(ctx context.Context, expectedResourceVersion int64, generation *model.DeltaGeneration) (*model.DeltaGeneration, error)
}

var _ Store = (*GenerationStore)(nil)

type GenerationKey struct {
	OrgID           uuid.UUID
	ImageRepository string
	SourceDigest    string
	TargetDigest    string
}

type generationGet struct {
	status *string
}

type GenerationGetOption func(*generationGet)

func WithStatus(status string) GenerationGetOption {
	return func(o *generationGet) {
		o.status = &status
	}
}

// GenerationStore persists delta generation resources.
type GenerationStore struct {
	dbHandler *gorm.DB
	log       logrus.FieldLogger
}

// NewStore creates a store for delta generations.
func NewStore(db *gorm.DB, log logrus.FieldLogger) *GenerationStore {
	return &GenerationStore{dbHandler: db, log: log}
}

func (s *GenerationStore) getDB(ctx context.Context) *gorm.DB {
	return s.dbHandler.WithContext(ctx)
}

// InitialMigration creates the delta generation resource table.
func (s *GenerationStore) InitialMigration(ctx context.Context) error {
	return s.getDB(ctx).AutoMigrate(&model.DeltaGeneration{})
}

// generationConflict returns the atomic upsert policy used when admitting a generation.
// Failed rows are requeued, eligible rejected rows are updated, and in-progress or
// succeeded rows keep their existing status and metadata.
func generationConflict() clause.OnConflict {
	statusArgs := map[string]interface{}{
		"pending":  model.DeltaGenerationPending,
		"failed":   model.DeltaGenerationFailed,
		"rejected": model.DeltaGenerationRejected,
	}
	namedExpr := func(sql string) clause.NamedExpr {
		return clause.NamedExpr{SQL: sql, Vars: []interface{}{statusArgs}}
	}
	return clause.OnConflict{
		Columns: []clause.Column{
			{Name: "org_id"},
			{Name: "image_repository"},
			{Name: "source_digest"},
			{Name: "target_digest"},
		},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"status": namedExpr(`
				CASE
					WHEN EXCLUDED.status = CAST(@rejected AS text)
					 AND delta_generations.status IN (
						CAST(@pending AS text),
						CAST(@failed AS text),
						CAST(@rejected AS text)
					 ) THEN CAST(@rejected AS text)
					WHEN delta_generations.status = CAST(@failed AS text)
					 THEN CAST(@pending AS text)
					ELSE delta_generations.status
				END`),
			"phase": namedExpr(`
				CASE
					WHEN delta_generations.status = CAST(@failed AS text) THEN NULL
					ELSE delta_generations.phase
				END`),
			"size_bytes": namedExpr(`
				CASE
					WHEN EXCLUDED.status = CAST(@rejected AS text)
					 AND delta_generations.status IN (
						CAST(@pending AS text),
						CAST(@failed AS text),
						CAST(@rejected AS text)
					 ) THEN EXCLUDED.size_bytes
					ELSE delta_generations.size_bytes
				END`),
			"resource_version": namedExpr(`
				CASE
					WHEN delta_generations.status = CAST(@failed AS text)
					  OR (
						EXCLUDED.status = CAST(@rejected AS text)
						AND delta_generations.status IN (
							CAST(@pending AS text),
							CAST(@failed AS text),
							CAST(@rejected AS text)
						)
					  ) THEN delta_generations.resource_version + 1
					ELSE delta_generations.resource_version
				END`),
			"updated_at": namedExpr(`
				CASE
					WHEN delta_generations.status = CAST(@failed AS text)
					  OR (
						EXCLUDED.status = CAST(@rejected AS text)
						AND delta_generations.status IN (
							CAST(@pending AS text),
							CAST(@failed AS text),
							CAST(@rejected AS text)
						)
					  ) THEN NOW()
					ELSE delta_generations.updated_at
				END`),
		}),
	}
}

func generationKeyOf(gen *model.DeltaGeneration) GenerationKey {
	return GenerationKey{
		OrgID:           gen.OrgID,
		ImageRepository: gen.ImageRepository,
		SourceDigest:    gen.SourceDigest,
		TargetDigest:    gen.TargetDigest,
	}
}

func uniqueGenerations(gens []*model.DeltaGeneration) ([]*model.DeltaGeneration, error) {
	for _, gen := range gens {
		if gen == nil {
			return nil, fmt.Errorf("cannot insert nil DeltaGeneration")
		}
	}
	return lo.UniqBy(gens, generationKeyOf), nil
}

func (s *GenerationStore) insertGenerations(ctx context.Context, gens []*model.DeltaGeneration) ([]model.DeltaGeneration, error) {
	gens, err := uniqueGenerations(gens)
	if err != nil {
		return nil, err
	}
	if len(gens) == 0 {
		return nil, nil
	}
	rows := make([]model.DeltaGeneration, len(gens))
	for i, gen := range gens {
		if gen.Status == "" {
			gen.Status = model.DeltaGenerationPending
		}
		rows[i] = *gen
	}
	result := s.getDB(ctx).Clauses(generationConflict(), clause.Returning{}).Create(&rows)
	if result.Error != nil {
		return nil, store.ErrorFromGormError(result.Error)
	}
	return rows, nil
}

func (s *GenerationStore) getGeneration(ctx context.Context, key GenerationKey, opts ...GenerationGetOption) (*model.DeltaGeneration, error) {
	cfg := &generationGet{}
	for _, opt := range opts {
		opt(cfg)
	}
	q := s.getDB(ctx).Where("org_id = ?", key.OrgID)
	if key.ImageRepository != "" {
		q = q.Where("image_repository = ?", key.ImageRepository)
	}
	if key.SourceDigest != "" {
		q = q.Where("source_digest = ?", key.SourceDigest)
	}
	if key.TargetDigest != "" {
		q = q.Where("target_digest = ?", key.TargetDigest)
	}
	if cfg.status != nil {
		q = q.Where("status = ?", *cfg.status)
	}
	uniqueKey := key.ImageRepository != "" && key.SourceDigest != "" && key.TargetDigest != ""
	if uniqueKey {
		var gen model.DeltaGeneration
		result := q.Take(&gen)
		if result.Error != nil {
			return nil, store.ErrorFromGormError(result.Error)
		}
		return &gen, nil
	}
	var gens []model.DeltaGeneration
	result := q.Order("updated_at DESC").Limit(2).Find(&gens)
	if result.Error != nil {
		return nil, store.ErrorFromGormError(result.Error)
	}
	if len(gens) == 0 {
		return nil, flterrors.ErrResourceNotFound
	}
	if len(gens) > 1 {
		return nil, fmt.Errorf("multiple generations matched org=%s repo=%s source=%s target=%s",
			key.OrgID, key.ImageRepository, key.SourceDigest, key.TargetDigest)
	}
	return &gens[0], nil
}

func (s *GenerationStore) InsertDeltaGenerations(ctx context.Context, generations []*model.DeltaGeneration) ([]model.DeltaGeneration, error) {
	return s.insertGenerations(ctx, generations)
}

func (s *GenerationStore) GetDeltaGeneration(ctx context.Context, key GenerationKey, opts ...GenerationGetOption) (*model.DeltaGeneration, error) {
	return s.getGeneration(ctx, key, opts...)
}

func (s *GenerationStore) ListDeltaGenerations(ctx context.Context, keys []GenerationKey) ([]model.DeltaGeneration, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	var generations []model.DeltaGeneration
	keys = lo.Uniq(keys)
	for start := 0; start < len(keys); start += generationListBatchSize {
		end := min(start+generationListBatchSize, len(keys))
		values := make([][]interface{}, 0, end-start)
		for _, key := range keys[start:end] {
			values = append(values, []interface{}{key.OrgID, key.ImageRepository, key.SourceDigest, key.TargetDigest})
		}
		var batch []model.DeltaGeneration
		if err := s.getDB(ctx).Where("(org_id, image_repository, source_digest, target_digest) IN ?", values).Find(&batch).Error; err != nil {
			return nil, store.ErrorFromGormError(err)
		}
		generations = append(generations, batch...)
	}
	return generations, nil
}

func (s *GenerationStore) UpdateDeltaGeneration(ctx context.Context, expectedResourceVersion int64, generation *model.DeltaGeneration) (*model.DeltaGeneration, error) {
	if generation == nil {
		return nil, fmt.Errorf("cannot update nil DeltaGeneration")
	}
	key := generationKeyOf(generation)
	var updated model.DeltaGeneration
	updates := map[string]interface{}{
		"status":           generation.Status,
		"phase":            generation.Phase,
		"delta_ref":        generation.DeltaRef,
		"size_bytes":       generation.SizeBytes,
		"last_verified_at": generation.LastVerifiedAt,
		"generated_at":     generation.GeneratedAt,
		"resource_version": gorm.Expr("resource_version + 1"),
		"updated_at":       gorm.Expr("NOW()"),
	}
	result := s.getDB(ctx).Model(&updated).Where(
		"org_id = ? AND image_repository = ? AND source_digest = ? AND target_digest = ? AND resource_version = ?",
		key.OrgID, key.ImageRepository, key.SourceDigest, key.TargetDigest, expectedResourceVersion,
	).Clauses(clause.Returning{}).Updates(updates)
	if result.Error != nil {
		return nil, store.ErrorFromGormError(result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, flterrors.ErrNoRowsUpdated
	}
	return &updated, nil
}
