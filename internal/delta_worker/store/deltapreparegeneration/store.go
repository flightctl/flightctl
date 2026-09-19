package deltapreparegeneration

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	deltagenerationstore "github.com/flightctl/flightctl/internal/delta_worker/store/deltageneration"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	prepareGenerationListBatchSize = 500
	prepareGenerationIndex         = "idx_delta_prepare_generations_generation"
	fkPrepareGenerationPrepare     = "fk_delta_prepare_generations_prepare"
	fkPrepareGenerationGeneration  = "fk_delta_prepare_generations_generation"
)

type Store interface {
	CreateDeltaPrepareGenerations(ctx context.Context, joins []*model.DeltaPrepareGeneration) (CreateDeltaPrepareGenerationsResult, error)
	ListDeltaPrepareGenerations(ctx context.Context, filter ListFilter) ([]model.DeltaPrepareGeneration, error)
}

var _ Store = (*PrepareGenerationStore)(nil)

type ListFilter struct {
	PrepareID     *uuid.UUID
	GenerationKey *deltagenerationstore.GenerationKey
}

// CreateDeltaPrepareGenerationsResult contains the joins inserted and the prepares updated by the transaction.
type CreateDeltaPrepareGenerationsResult struct {
	InsertedJoins   []*model.DeltaPrepareGeneration
	UpdatedPrepares map[uuid.UUID]model.DeltaPrepare
}

// PrepareGenerationStore persists the prepare-generation join resources.
type PrepareGenerationStore struct {
	dbHandler *gorm.DB
	log       logrus.FieldLogger
}

// NewStore creates a store for prepare-generation joins.
func NewStore(db *gorm.DB, log logrus.FieldLogger) *PrepareGenerationStore {
	return &PrepareGenerationStore{dbHandler: db, log: log}
}

func (s *PrepareGenerationStore) getDB(ctx context.Context) *gorm.DB {
	return s.dbHandler.WithContext(ctx)
}

// InitialMigration creates the prepare-generation join table, index, and foreign keys.
// The generation and prepare stores must be migrated before this store.
func (s *PrepareGenerationStore) InitialMigration(ctx context.Context) error {
	db := s.getDB(ctx)
	if err := db.AutoMigrate(&model.DeltaPrepareGeneration{}); err != nil {
		return err
	}
	if db.Dialector.Name() != "postgres" {
		return nil
	}
	if !db.Migrator().HasIndex(&model.DeltaPrepareGeneration{}, prepareGenerationIndex) {
		if err := db.Exec(`
			CREATE INDEX IF NOT EXISTS idx_delta_prepare_generations_generation
			ON delta_prepare_generations (org_id, image_repository, source_digest, target_digest, prepare_id)
		`).Error; err != nil {
			return err
		}
	}
	if !db.Migrator().HasConstraint(&model.DeltaPrepareGeneration{}, fkPrepareGenerationPrepare) {
		if err := db.Exec(`
			ALTER TABLE delta_prepare_generations
			ADD CONSTRAINT fk_delta_prepare_generations_prepare
			FOREIGN KEY (prepare_id) REFERENCES delta_prepares (id)
		`).Error; err != nil {
			return err
		}
	}
	if !db.Migrator().HasConstraint(&model.DeltaPrepareGeneration{}, fkPrepareGenerationGeneration) {
		if err := db.Exec(`
			ALTER TABLE delta_prepare_generations
			ADD CONSTRAINT fk_delta_prepare_generations_generation
			FOREIGN KEY (org_id, image_repository, source_digest, target_digest)
			REFERENCES delta_generations (org_id, image_repository, source_digest, target_digest)
		`).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *PrepareGenerationStore) CreateDeltaPrepareGenerations(ctx context.Context, joins []*model.DeltaPrepareGeneration) (CreateDeltaPrepareGenerationsResult, error) {
	if len(joins) == 0 {
		return CreateDeltaPrepareGenerationsResult{}, nil
	}
	for _, join := range joins {
		if join == nil {
			return CreateDeltaPrepareGenerationsResult{}, fmt.Errorf("cannot insert nil DeltaPrepareGeneration")
		}
		join.Completed = false
	}

	inserted := make([]*model.DeltaPrepareGeneration, 0, len(joins))
	updatedPrepares := make(map[uuid.UUID]model.DeltaPrepare)
	err := store.RunInTransaction(ctx, s.dbHandler, func(tx *gorm.DB) error {
		prepareIDs := make(map[uuid.UUID]struct{}, len(joins))
		for _, join := range joins {
			prepareIDs[join.PrepareID] = struct{}{}
			result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(join)
			if result.Error != nil {
				return store.ErrorFromGormError(result.Error)
			}
			if result.RowsAffected == 1 {
				inserted = append(inserted, join)
			}
		}
		affectedPrepareIDs := make([]uuid.UUID, 0, len(prepareIDs))
		for prepareID := range prepareIDs {
			affectedPrepareIDs = append(affectedPrepareIDs, prepareID)
		}

		// Count the generations that are still pending in the same
		// transaction as the joins. A generation can finish before its
		// join is committed; excluding it here prevents a missed wake-up.
		// Compute all affected prepares in one grouped update instead of
		// issuing one update per prepare.
		var updated []model.DeltaPrepare
		result := tx.Raw(`
WITH pending_counts AS (
	SELECT pg.prepare_id,
		CAST(COUNT(*) FILTER (
			WHERE g.status IS NULL OR g.status NOT IN (@succeeded, @failed, @rejected)
		) AS INTEGER) AS pending_count
	FROM delta_prepare_generations AS pg
	LEFT JOIN delta_generations AS g
		ON g.org_id = pg.org_id
		AND g.image_repository = pg.image_repository
		AND g.source_digest = pg.source_digest
		AND g.target_digest = pg.target_digest
	WHERE pg.prepare_id IN @prepare_ids
	GROUP BY pg.prepare_id
)
UPDATE delta_prepares AS p
SET pending_generations_count = pc.pending_count,
	status = CASE WHEN pc.pending_count = 0 THEN @complete ELSE p.status END,
	resource_version = p.resource_version + 1
FROM pending_counts AS pc
WHERE p.id = pc.prepare_id
	AND p.status = @waiting
RETURNING *`, map[string]interface{}{
			"succeeded":   model.DeltaGenerationSucceeded,
			"failed":      model.DeltaGenerationFailed,
			"rejected":    model.DeltaGenerationRejected,
			"prepare_ids": affectedPrepareIDs,
			"complete":    model.DeltaPrepareComplete,
			"waiting":     model.DeltaPrepareWaiting,
		}).Scan(&updated)
		if result.Error != nil {
			return store.ErrorFromGormError(result.Error)
		}
		for _, prepare := range updated {
			updatedPrepares[prepare.ID] = prepare
		}
		return nil
	})
	if err != nil {
		return CreateDeltaPrepareGenerationsResult{}, err
	}
	return CreateDeltaPrepareGenerationsResult{InsertedJoins: inserted, UpdatedPrepares: updatedPrepares}, nil
}

func (s *PrepareGenerationStore) ListDeltaPrepareGenerations(ctx context.Context, filter ListFilter) ([]model.DeltaPrepareGeneration, error) {
	query := s.getDB(ctx)
	if filter.PrepareID != nil {
		query = query.Where("prepare_id = ?", *filter.PrepareID)
	}
	if filter.GenerationKey != nil {
		key := filter.GenerationKey
		query = query.Where("org_id = ? AND image_repository = ? AND source_digest = ? AND target_digest = ?", key.OrgID, key.ImageRepository, key.SourceDigest, key.TargetDigest)
	}
	var rows []model.DeltaPrepareGeneration
	query = query.Order("prepare_id ASC, org_id ASC, image_repository ASC, source_digest ASC, target_digest ASC")
	var cursor *model.DeltaPrepareGeneration
	for {
		page := query
		if cursor != nil {
			page = page.Where(
				"(prepare_id, org_id, image_repository, source_digest, target_digest) > (?, ?, ?, ?, ?)",
				cursor.PrepareID, cursor.OrgID, cursor.ImageRepository, cursor.SourceDigest, cursor.TargetDigest,
			)
		}
		var batch []model.DeltaPrepareGeneration
		result := page.Limit(prepareGenerationListBatchSize).Find(&batch)
		if result.Error != nil {
			return nil, store.ErrorFromGormError(result.Error)
		}
		rows = append(rows, batch...)
		if len(batch) < prepareGenerationListBatchSize {
			break
		}
		cursor = &batch[len(batch)-1]
	}
	return rows, nil
}
