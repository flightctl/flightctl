package delta

import (
	"context"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const waitingPrepareIndex = "idx_delta_prepares_one_waiting"

const (
	fkPrepareGenerationPrepare    = "fk_delta_prepare_generations_prepare"
	fkPrepareGenerationGeneration = "fk_delta_prepare_generations_generation"
)

type Store interface {
	InitialMigration(ctx context.Context) error
	InsertGeneration(ctx context.Context, gen *model.DeltaGeneration) (changed bool, err error)
	GetGeneration(ctx context.Context, key GenerationKey) (*model.DeltaGeneration, error)
	CASGeneration(ctx context.Context, key GenerationKey, expectedRV int64, update GenerationCAS) error
	InsertPrepare(ctx context.Context, prep *model.DeltaPrepare) error
	GetPrepare(ctx context.Context, id uuid.UUID) (*model.DeltaPrepare, error)
	CASPrepareStatus(ctx context.Context, id uuid.UUID, to string) error
	ListWaitingPastDeadline(ctx context.Context) ([]model.DeltaPrepare, error)
	JoinPrepareGeneration(ctx context.Context, prepareID uuid.UUID, key GenerationKey) error
}

type GenerationKey struct {
	OrgID           uuid.UUID
	ImageRepository string
	SourceDigest    string
	TargetDigest    string
}

type GenerationCAS struct {
	Status         string
	DeltaRef       *string
	SizeBytes      *int64
	LastVerifiedAt *time.Time
	GeneratedAt    *time.Time
}

type DeltaStore struct {
	dbHandler *gorm.DB
	log       logrus.FieldLogger
}

var _ Store = (*DeltaStore)(nil)

func NewStore(db *gorm.DB, log logrus.FieldLogger) Store {
	return &DeltaStore{dbHandler: db, log: log}
}

func (s *DeltaStore) getDB(ctx context.Context) *gorm.DB {
	return s.dbHandler.WithContext(ctx)
}

func (s *DeltaStore) InitialMigration(ctx context.Context) error {
	db := s.getDB(ctx)
	if err := db.AutoMigrate(
		&model.DeltaGeneration{},
		&model.DeltaPrepare{},
		&model.DeltaPrepareGeneration{},
	); err != nil {
		return err
	}
	if err := s.createWaitingPrepareIndex(db); err != nil {
		return err
	}
	return s.createJoinForeignKeys(db)
}

func (s *DeltaStore) createWaitingPrepareIndex(db *gorm.DB) error {
	if db.Migrator().HasIndex(&model.DeltaPrepare{}, waitingPrepareIndex) {
		return nil
	}
	if db.Dialector.Name() != "postgres" {
		return nil
	}
	return db.Exec(`
		CREATE UNIQUE INDEX IF NOT EXISTS ` + waitingPrepareIndex + `
		ON delta_prepares (org_id, kind, name)
		WHERE status = 'waiting'
	`).Error
}

func (s *DeltaStore) createJoinForeignKeys(db *gorm.DB) error {
	if db.Dialector.Name() != "postgres" {
		return nil
	}
	if !db.Migrator().HasConstraint(&model.DeltaPrepareGeneration{}, fkPrepareGenerationPrepare) {
		if err := db.Exec(`
			ALTER TABLE delta_prepare_generations
			ADD CONSTRAINT ` + fkPrepareGenerationPrepare + `
			FOREIGN KEY (prepare_id) REFERENCES delta_prepares (id)
		`).Error; err != nil {
			return err
		}
	}
	if !db.Migrator().HasConstraint(&model.DeltaPrepareGeneration{}, fkPrepareGenerationGeneration) {
		if err := db.Exec(`
			ALTER TABLE delta_prepare_generations
			ADD CONSTRAINT ` + fkPrepareGenerationGeneration + `
			FOREIGN KEY (org_id, image_repository, source_digest, target_digest)
			REFERENCES delta_generations (org_id, image_repository, source_digest, target_digest)
		`).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *DeltaStore) InsertGeneration(ctx context.Context, gen *model.DeltaGeneration) (bool, error) {
	if gen == nil {
		return false, fmt.Errorf("cannot insert nil DeltaGeneration")
	}
	if gen.Status == "" {
		gen.Status = model.DeltaGenerationPending
	}
	result := s.getDB(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "org_id"},
			{Name: "image_repository"},
			{Name: "source_digest"},
			{Name: "target_digest"},
		},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"status":           model.DeltaGenerationPending,
			"resource_version": gorm.Expr("delta_generations.resource_version + 1"),
			"updated_at":       gorm.Expr("NOW()"),
		}),
		Where: clause.Where{Exprs: []clause.Expression{
			clause.Eq{Column: clause.Column{Table: "delta_generations", Name: "status"}, Value: model.DeltaGenerationFailed},
		}},
	}).Create(gen)
	if result.Error != nil {
		return false, store.ErrorFromGormError(result.Error)
	}
	return result.RowsAffected == 1, nil
}

func (s *DeltaStore) GetGeneration(ctx context.Context, key GenerationKey) (*model.DeltaGeneration, error) {
	var gen model.DeltaGeneration
	result := s.getDB(ctx).Where(
		"org_id = ? AND image_repository = ? AND source_digest = ? AND target_digest = ?",
		key.OrgID, key.ImageRepository, key.SourceDigest, key.TargetDigest,
	).Take(&gen)
	if result.Error != nil {
		return nil, store.ErrorFromGormError(result.Error)
	}
	return &gen, nil
}

func (s *DeltaStore) InsertPrepare(ctx context.Context, prep *model.DeltaPrepare) error {
	if prep == nil {
		return fmt.Errorf("cannot insert nil DeltaPrepare")
	}
	if prep.ID == uuid.Nil {
		prep.ID = uuid.New()
	}
	if prep.Status == "" {
		prep.Status = model.DeltaPrepareWaiting
	}
	return store.ErrorFromGormError(s.getDB(ctx).Create(prep).Error)
}

func (s *DeltaStore) GetPrepare(ctx context.Context, id uuid.UUID) (*model.DeltaPrepare, error) {
	var prep model.DeltaPrepare
	result := s.getDB(ctx).Where("id = ?", id).Take(&prep)
	if result.Error != nil {
		return nil, store.ErrorFromGormError(result.Error)
	}
	return &prep, nil
}

func (s *DeltaStore) JoinPrepareGeneration(ctx context.Context, prepareID uuid.UUID, key GenerationKey) error {
	join := &model.DeltaPrepareGeneration{
		PrepareID:       prepareID,
		OrgID:           key.OrgID,
		ImageRepository: key.ImageRepository,
		SourceDigest:    key.SourceDigest,
		TargetDigest:    key.TargetDigest,
	}
	return store.ErrorFromGormError(s.getDB(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(join).Error)
}

func (s *DeltaStore) ListWaitingPastDeadline(ctx context.Context) ([]model.DeltaPrepare, error) {
	var rows []model.DeltaPrepare
	result := s.getDB(ctx).Where(
		"status = ? AND deadline IS NOT NULL AND deadline < NOW()",
		model.DeltaPrepareWaiting,
	).Find(&rows)
	if result.Error != nil {
		return nil, store.ErrorFromGormError(result.Error)
	}
	return rows, nil
}

func (s *DeltaStore) CASPrepareStatus(ctx context.Context, id uuid.UUID, to string) error {
	result := s.getDB(ctx).Model(&model.DeltaPrepare{}).
		Where("id = ? AND status = ?", id, model.DeltaPrepareWaiting).
		Update("status", to)
	if result.Error != nil {
		return store.ErrorFromGormError(result.Error)
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
		).Updates(updates)
	if result.Error != nil {
		return store.ErrorFromGormError(result.Error)
	}
	if result.RowsAffected == 0 {
		return flterrors.ErrNoRowsUpdated
	}
	return nil
}
