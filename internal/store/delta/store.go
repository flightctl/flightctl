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
	InsertGenerations(ctx context.Context, gens []*model.DeltaGeneration) (changed []GenerationKey, err error)
	InsertRejectedGeneration(ctx context.Context, gen *model.DeltaGeneration) error
	GetGeneration(ctx context.Context, key GenerationKey) (*model.DeltaGeneration, error)
	ClaimGeneration(ctx context.Context, key GenerationKey) (*model.DeltaGeneration, error)
	CASGeneration(ctx context.Context, key GenerationKey, expectedRV int64, update GenerationCAS) error
	InsertPrepare(ctx context.Context, prep *model.DeltaPrepare) error
	GetPrepare(ctx context.Context, id uuid.UUID) (*model.DeltaPrepare, error)
	CASPrepareStatus(ctx context.Context, id uuid.UUID, to string) error
	ListWaitingPastDeadline(ctx context.Context) ([]model.DeltaPrepare, error)
	ListWaitingPreparesByGeneration(ctx context.Context, key GenerationKey) ([]model.DeltaPrepare, error)
	InsertPrepareGenerations(ctx context.Context, prepareID uuid.UUID, keys []GenerationKey) error
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

func rejectedConflictStatuses() []string {
	return []string{
		model.DeltaGenerationPending,
		model.DeltaGenerationFailed,
		model.DeltaGenerationRejected,
	}
}

func rejectedConflictAllows(status string) bool {
	for _, allowed := range rejectedConflictStatuses() {
		if status == allowed {
			return true
		}
	}
	return false
}

func isClaimableStatus(status string) bool {
	return status == model.DeltaGenerationPending
}

func rejectedConflict() clause.OnConflict {
	values := make([]interface{}, 0, len(rejectedConflictStatuses()))
	for _, status := range rejectedConflictStatuses() {
		values = append(values, status)
	}
	return clause.OnConflict{
		Columns: []clause.Column{
			{Name: "org_id"},
			{Name: "image_repository"},
			{Name: "source_digest"},
			{Name: "target_digest"},
		},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"status":           model.DeltaGenerationRejected,
			"size_bytes":       gorm.Expr("EXCLUDED.size_bytes"),
			"resource_version": gorm.Expr("delta_generations.resource_version + 1"),
			"updated_at":       gorm.Expr("NOW()"),
		}),
		Where: clause.Where{Exprs: []clause.Expression{
			clause.IN{
				Column: clause.Column{Table: "delta_generations", Name: "status"},
				Values: values,
			},
		}},
	}
}

func generationConflict() clause.OnConflict {
	return clause.OnConflict{
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
	out := make([]*model.DeltaGeneration, 0, len(gens))
	seen := make(map[GenerationKey]struct{}, len(gens))
	for _, gen := range gens {
		if gen == nil {
			return nil, fmt.Errorf("cannot insert nil DeltaGeneration")
		}
		key := generationKeyOf(gen)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, gen)
	}
	return out, nil
}

func uniqueGenerationKeys(keys []GenerationKey) []GenerationKey {
	out := make([]GenerationKey, 0, len(keys))
	seen := make(map[GenerationKey]struct{}, len(keys))
	for _, key := range keys {
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out
}

func (s *DeltaStore) InsertGenerations(ctx context.Context, gens []*model.DeltaGeneration) ([]GenerationKey, error) {
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
	returning := clause.Returning{
		Columns: []clause.Column{
			{Name: "org_id"},
			{Name: "image_repository"},
			{Name: "source_digest"},
			{Name: "target_digest"},
		},
	}
	dry := s.getDB(ctx).Session(&gorm.Session{DryRun: true}).Clauses(generationConflict(), returning).Create(&rows)
	if dry.Error != nil {
		return nil, store.ErrorFromGormError(dry.Error)
	}
	var changed []GenerationKey
	if err := s.getDB(ctx).Raw(dry.Statement.SQL.String(), dry.Statement.Vars...).Scan(&changed).Error; err != nil {
		return nil, store.ErrorFromGormError(err)
	}
	return changed, nil
}

func (s *DeltaStore) InsertRejectedGeneration(ctx context.Context, gen *model.DeltaGeneration) error {
	if gen == nil {
		return fmt.Errorf("cannot insert nil DeltaGeneration")
	}
	gen.Status = model.DeltaGenerationRejected
	return store.ErrorFromGormError(s.getDB(ctx).Clauses(rejectedConflict()).Create(gen).Error)
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

func (s *DeltaStore) ClaimGeneration(ctx context.Context, key GenerationKey) (*model.DeltaGeneration, error) {
	result := s.getDB(ctx).Model(&model.DeltaGeneration{}).
		Where(
			"org_id = ? AND image_repository = ? AND source_digest = ? AND target_digest = ? AND status = ?",
			key.OrgID, key.ImageRepository, key.SourceDigest, key.TargetDigest, model.DeltaGenerationPending,
		).
		Updates(map[string]interface{}{
			"status":           model.DeltaGenerationInProgress,
			"resource_version": gorm.Expr("resource_version + 1"),
		})
	if result.Error != nil {
		return nil, store.ErrorFromGormError(result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, flterrors.ErrNoRowsUpdated
	}
	return s.GetGeneration(ctx, key)
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

func (s *DeltaStore) InsertPrepareGenerations(ctx context.Context, prepareID uuid.UUID, keys []GenerationKey) error {
	keys = uniqueGenerationKeys(keys)
	if len(keys) == 0 {
		return nil
	}
	joins := make([]model.DeltaPrepareGeneration, len(keys))
	for i, key := range keys {
		joins[i] = model.DeltaPrepareGeneration{
			PrepareID:       prepareID,
			OrgID:           key.OrgID,
			ImageRepository: key.ImageRepository,
			SourceDigest:    key.SourceDigest,
			TargetDigest:    key.TargetDigest,
		}
	}
	return store.ErrorFromGormError(s.getDB(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&joins).Error)
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

func (s *DeltaStore) ListWaitingPreparesByGeneration(ctx context.Context, key GenerationKey) ([]model.DeltaPrepare, error) {
	var rows []model.DeltaPrepare
	result := s.getDB(ctx).Model(&model.DeltaPrepare{}).
		Joins("INNER JOIN delta_prepare_generations ON delta_prepare_generations.prepare_id = delta_prepares.id").
		Where(
			"delta_prepare_generations.org_id = ? AND delta_prepare_generations.image_repository = ? AND delta_prepare_generations.source_digest = ? AND delta_prepare_generations.target_digest = ? AND delta_prepares.status = ?",
			key.OrgID, key.ImageRepository, key.SourceDigest, key.TargetDigest, model.DeltaPrepareWaiting,
		).
		Find(&rows)
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
