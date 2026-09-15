package store

import (
	"context"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	"github.com/flightctl/flightctl/internal/flterrors"
	storepkg "github.com/flightctl/flightctl/internal/store"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"gorm.io/gorm"
)

// Store preserves the pre-service task contract while the worker consumers
// migrate to the resource-oriented service interfaces. It is intentionally
// local to this extraction layer and is removed once the follow-up wiring lands.
type Store interface {
	InitialMigration(ctx context.Context) error
	// InsertGenerations returns keys for every row returned by the upsert,
	// including existing rows matched by conflict handling.
	InsertGenerations(ctx context.Context, gens []*model.DeltaGeneration) ([]GenerationKey, error)
	InsertRejectedGeneration(ctx context.Context, gen *model.DeltaGeneration) error
	GetGeneration(ctx context.Context, key GenerationKey, opts ...GenerationGetOption) (*model.DeltaGeneration, error)
	ClaimGeneration(ctx context.Context, key GenerationKey) (*model.DeltaGeneration, error)
	CASGeneration(ctx context.Context, key GenerationKey, expectedRV int64, update GenerationCAS) error
	InsertPrepare(ctx context.Context, prep *model.DeltaPrepare) error
	GetPrepare(ctx context.Context, id uuid.UUID) (*model.DeltaPrepare, error)
	CASPrepareStatus(ctx context.Context, id uuid.UUID, to string) error
	ListWaitingPastDeadline(ctx context.Context, limit int, asOf time.Time) ([]model.DeltaPrepare, error)
	ListWaitingPreparesByGeneration(ctx context.Context, key GenerationKey) ([]model.DeltaPrepare, error)
	InsertPrepareGenerations(ctx context.Context, prepareID uuid.UUID, keys []GenerationKey) error
}

type GenerationCAS struct {
	Status         string
	DeltaRef       *string
	SizeBytes      *int64
	LastVerifiedAt *time.Time
	GeneratedAt    *time.Time
}

var _ Store = (*DeltaStore)(nil)

func (s *DeltaStore) InsertGenerations(ctx context.Context, gens []*model.DeltaGeneration) ([]GenerationKey, error) {
	rows, err := s.InsertDeltaGenerations(ctx, gens)
	if err != nil {
		return nil, err
	}
	keys := make([]GenerationKey, 0, len(rows))
	for i := range rows {
		keys = append(keys, generationKeyOf(&rows[i]))
	}
	return keys, nil
}

func (s *DeltaStore) InsertRejectedGeneration(ctx context.Context, gen *model.DeltaGeneration) error {
	if gen == nil {
		return fmt.Errorf("cannot insert nil DeltaGeneration")
	}
	gen.Status = model.DeltaGenerationRejected
	_, err := s.InsertDeltaGenerations(ctx, []*model.DeltaGeneration{gen})
	return err
}

func (s *DeltaStore) GetGeneration(ctx context.Context, key GenerationKey, opts ...GenerationGetOption) (*model.DeltaGeneration, error) {
	return s.GetDeltaGeneration(ctx, key, opts...)
}

func (s *DeltaStore) ClaimGeneration(ctx context.Context, key GenerationKey) (*model.DeltaGeneration, error) {
	result := s.getDB(ctx).Model(&model.DeltaGeneration{}).
		Where("org_id = ? AND image_repository = ? AND source_digest = ? AND target_digest = ? AND status = ?",
			key.OrgID, key.ImageRepository, key.SourceDigest, key.TargetDigest, model.DeltaGenerationPending).
		Updates(map[string]interface{}{
			"status":           model.DeltaGenerationInProgress,
			"resource_version": gorm.Expr("resource_version + 1"),
		})
	if result.Error != nil {
		return nil, storepkg.ErrorFromGormError(result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, flterrors.ErrNoRowsUpdated
	}
	return s.GetDeltaGeneration(ctx, key)
}

func (s *DeltaStore) InsertPrepare(ctx context.Context, prep *model.DeltaPrepare) error {
	return s.CreateDeltaPrepare(ctx, prep)
}

func (s *DeltaStore) GetPrepare(ctx context.Context, id uuid.UUID) (*model.DeltaPrepare, error) {
	return s.GetDeltaPrepare(ctx, PrepareKey{ID: id})
}

func (s *DeltaStore) InsertPrepareGenerations(ctx context.Context, prepareID uuid.UUID, keys []GenerationKey) error {
	keys = lo.Uniq(keys)
	if len(keys) == 0 {
		return nil
	}
	joins := make([]*model.DeltaPrepareGeneration, len(keys))
	for i, key := range keys {
		joins[i] = &model.DeltaPrepareGeneration{
			PrepareID:       prepareID,
			OrgID:           key.OrgID,
			ImageRepository: key.ImageRepository,
			SourceDigest:    key.SourceDigest,
			TargetDigest:    key.TargetDigest,
		}
	}
	_, err := s.CreateDeltaPrepareGenerations(ctx, joins)
	return err
}

func (s *DeltaStore) ListWaitingPreparesByGeneration(ctx context.Context, key GenerationKey) ([]model.DeltaPrepare, error) {
	var rows []model.DeltaPrepare
	result := s.getDB(ctx).Model(&model.DeltaPrepare{}).
		Joins("INNER JOIN delta_prepare_generations ON delta_prepare_generations.prepare_id = delta_prepares.id").
		Where(
			"delta_prepare_generations.org_id = ? AND delta_prepare_generations.image_repository = ? AND delta_prepare_generations.source_digest = ? AND delta_prepare_generations.target_digest = ? AND delta_prepares.status = ?",
			key.OrgID, key.ImageRepository, key.SourceDigest, key.TargetDigest, model.DeltaPrepareWaiting,
		).
		Limit(1000).
		Find(&rows)
	if result.Error != nil {
		return nil, storepkg.ErrorFromGormError(result.Error)
	}
	return rows, nil
}

func (s *DeltaStore) CASPrepareStatus(ctx context.Context, id uuid.UUID, to string) error {
	result := s.getDB(ctx).Model(&model.DeltaPrepare{}).
		Where("id = ? AND status = ?", id, model.DeltaPrepareWaiting).
		Updates(map[string]interface{}{
			"status":           to,
			"resource_version": gorm.Expr("resource_version + 1"),
		})
	if result.Error != nil {
		return storepkg.ErrorFromGormError(result.Error)
	}
	if result.RowsAffected == 0 {
		return flterrors.ErrNoRowsUpdated
	}
	return nil
}

func (s *DeltaStore) CASGeneration(ctx context.Context, key GenerationKey, expectedRV int64, update GenerationCAS) error {
	updates := map[string]interface{}{
		"status":           update.Status,
		"delta_ref":        update.DeltaRef,
		"size_bytes":       update.SizeBytes,
		"last_verified_at": update.LastVerifiedAt,
		"generated_at":     update.GeneratedAt,
		"resource_version": gorm.Expr("resource_version + 1"),
	}
	result := s.getDB(ctx).Model(&model.DeltaGeneration{}).
		Where(
			"org_id = ? AND image_repository = ? AND source_digest = ? AND target_digest = ? AND resource_version = ?",
			key.OrgID, key.ImageRepository, key.SourceDigest, key.TargetDigest, expectedRV,
		).
		Updates(updates)
	if result.Error != nil {
		return storepkg.ErrorFromGormError(result.Error)
	}
	if result.RowsAffected == 0 {
		return flterrors.ErrNoRowsUpdated
	}
	return nil
}
