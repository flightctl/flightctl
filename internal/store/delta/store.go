package delta

import (
	"context"
	"fmt"

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
}

type GenerationKey struct {
	OrgID           uuid.UUID
	ImageRepository string
	SourceDigest    string
	TargetDigest    string
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
