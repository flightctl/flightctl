package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	waitingPrepareIndex         = "idx_delta_prepares_one_waiting"
	waitingPrepareDeadlineIndex = "idx_delta_prepares_waiting_deadline"
	prepareGenerationIndex      = "idx_delta_prepare_generations_generation"

	fkPrepareGenerationPrepare    = "fk_delta_prepare_generations_prepare"
	fkPrepareGenerationGeneration = "fk_delta_prepare_generations_generation"

	MaxListWaitingPastDeadline = 1000
	deltaListBatchSize         = 500
)

// DeltaPrepareStore is the resource-oriented persistence API used by the
// prepare service. Updates replace the complete object under CAS.
type DeltaPrepareStore interface {
	CreateDeltaPrepare(ctx context.Context, prepare *model.DeltaPrepare) error
	CreateOrReplaceWaitingDeltaPrepare(ctx context.Context, prepare *model.DeltaPrepare) (PrepareAdmission, error)
	GetDeltaPrepare(ctx context.Context, key PrepareKey, opts ...PrepareGetOption) (*model.DeltaPrepare, error)
	ListDeltaPrepares(ctx context.Context, ids []uuid.UUID) ([]model.DeltaPrepare, error)
	UpdateDeltaPrepare(ctx context.Context, expectedResourceVersion int64, prepare *model.DeltaPrepare) (*model.DeltaPrepare, error)
	CountDeltaPrepareGenerations(ctx context.Context, prepareID uuid.UUID) (completed, total int, err error)
}

// DeltaGenerationStore is the resource-oriented persistence API used by the
// generation service. Updates replace the complete object under CAS.
type DeltaGenerationStore interface {
	InsertDeltaGenerations(ctx context.Context, generations []*model.DeltaGeneration) ([]model.DeltaGeneration, error)
	GetDeltaGeneration(ctx context.Context, key GenerationKey, opts ...GenerationGetOption) (*model.DeltaGeneration, error)
	ListDeltaGenerations(ctx context.Context, keys []GenerationKey) ([]model.DeltaGeneration, error)
	UpdateDeltaGeneration(ctx context.Context, expectedResourceVersion int64, generation *model.DeltaGeneration) (*model.DeltaGeneration, error)
}

type DeltaPrepareGenerationStore interface {
	CreateDeltaPrepareGenerations(ctx context.Context, joins []*model.DeltaPrepareGeneration) ([]*model.DeltaPrepareGeneration, error)
	ListDeltaPrepareGenerations(ctx context.Context, filter DeltaPrepareGenerationListFilter) ([]model.DeltaPrepareGeneration, error)
}

var _ DeltaPrepareStore = (*DeltaStore)(nil)
var _ DeltaGenerationStore = (*DeltaStore)(nil)

type DeltaPrepareGenerationListFilter struct {
	PrepareID     *uuid.UUID
	GenerationKey *GenerationKey
}

type GenerationKey struct {
	OrgID           uuid.UUID
	ImageRepository string
	SourceDigest    string
	TargetDigest    string
}

type PrepareKey struct {
	ID    uuid.UUID
	OrgID uuid.UUID
	Kind  string
	Name  string
}

// PrepareAdmission describes the result of atomically making a prepare the
// current waiting prepare for a resource.
type PrepareAdmission struct {
	Prepare  *model.DeltaPrepare
	Accepted bool
	Replaced bool
}

type prepareGet struct {
	status *string
}

type PrepareGetOption func(*prepareGet)

func WithPrepareStatus(status string) PrepareGetOption {
	return func(o *prepareGet) {
		o.status = &status
	}
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

type DeltaStore struct {
	dbHandler *gorm.DB
	log       logrus.FieldLogger
}

func NewStore(db *gorm.DB, log logrus.FieldLogger) *DeltaStore {
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
	if err := s.createWaitingPrepareDeadlineIndex(db); err != nil {
		return err
	}
	if err := s.createPrepareGenerationIndex(db); err != nil {
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
		CREATE UNIQUE INDEX IF NOT EXISTS idx_delta_prepares_one_waiting
		ON delta_prepares (org_id, kind, name)
		WHERE status = 'waiting'
	`).Error
}

func (s *DeltaStore) createWaitingPrepareDeadlineIndex(db *gorm.DB) error {
	if db.Migrator().HasIndex(&model.DeltaPrepare{}, waitingPrepareDeadlineIndex) {
		return nil
	}
	if db.Dialector.Name() != "postgres" {
		return nil
	}
	if err := db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_delta_prepares_waiting_deadline
		ON delta_prepares (deadline, id)
		WHERE status = 'waiting' AND deadline IS NOT NULL
	`).Error; err != nil {
		return fmt.Errorf("create waiting prepare deadline index: %w", err)
	}
	return nil
}

func (s *DeltaStore) createPrepareGenerationIndex(db *gorm.DB) error {
	if db.Migrator().HasIndex(&model.DeltaPrepareGeneration{}, prepareGenerationIndex) {
		return nil
	}
	if db.Dialector.Name() != "postgres" {
		return nil
	}
	return db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_delta_prepare_generations_generation
		ON delta_prepare_generations (org_id, image_repository, source_digest, target_digest, prepare_id)
	`).Error
}

func (s *DeltaStore) createJoinForeignKeys(db *gorm.DB) error {
	if db.Dialector.Name() != "postgres" {
		return nil
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

func generationConflict() clause.OnConflict {
	// These values are fixed model constants, so embedding escaped SQL string
	// literals avoids PostgreSQL treating the repeated CASE parameters as
	// untyped placeholders while building the ON CONFLICT expression.
	sqlStatus := func(status string) string {
		return "'" + strings.ReplaceAll(status, "'", "''") + "'"
	}
	resetFailed := "delta_generations.status = " + sqlStatus(model.DeltaGenerationFailed)
	applyRejected := "EXCLUDED.status = " + sqlStatus(model.DeltaGenerationRejected) + " AND delta_generations.status IN (" +
		sqlStatus(model.DeltaGenerationPending) + ", " + sqlStatus(model.DeltaGenerationFailed) + ", " + sqlStatus(model.DeltaGenerationRejected) + ")"
	changed := "(" + resetFailed + " OR " + applyRejected + ")"
	returningStatus := gorm.Expr(
		"CASE WHEN " + applyRejected + " THEN " + sqlStatus(model.DeltaGenerationRejected) + " WHEN " + resetFailed + " THEN " + sqlStatus(model.DeltaGenerationPending) + " ELSE delta_generations.status END",
	)
	return clause.OnConflict{
		Columns: []clause.Column{
			{Name: "org_id"},
			{Name: "image_repository"},
			{Name: "source_digest"},
			{Name: "target_digest"},
		},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"status": returningStatus,
			"size_bytes": gorm.Expr(
				"CASE WHEN " + applyRejected + " THEN EXCLUDED.size_bytes ELSE delta_generations.size_bytes END",
			),
			"resource_version": gorm.Expr(
				"CASE WHEN " + changed + " THEN delta_generations.resource_version + 1 ELSE delta_generations.resource_version END",
			),
			"updated_at": gorm.Expr(
				"CASE WHEN " + changed + " THEN NOW() ELSE delta_generations.updated_at END",
			),
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

func (s *DeltaStore) insertGenerations(ctx context.Context, gens []*model.DeltaGeneration) ([]model.DeltaGeneration, error) {
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

func (s *DeltaStore) getGeneration(ctx context.Context, key GenerationKey, opts ...GenerationGetOption) (*model.DeltaGeneration, error) {
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

func (s *DeltaStore) InsertDeltaGenerations(ctx context.Context, generations []*model.DeltaGeneration) ([]model.DeltaGeneration, error) {
	return s.insertGenerations(ctx, generations)
}

func (s *DeltaStore) GetDeltaGeneration(ctx context.Context, key GenerationKey, opts ...GenerationGetOption) (*model.DeltaGeneration, error) {
	return s.getGeneration(ctx, key, opts...)
}

func (s *DeltaStore) ListDeltaGenerations(ctx context.Context, keys []GenerationKey) ([]model.DeltaGeneration, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	var generations []model.DeltaGeneration
	keys = lo.Uniq(keys)
	for start := 0; start < len(keys); start += deltaListBatchSize {
		end := min(start+deltaListBatchSize, len(keys))
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

func (s *DeltaStore) UpdateDeltaGeneration(ctx context.Context, expectedResourceVersion int64, generation *model.DeltaGeneration) (*model.DeltaGeneration, error) {
	if generation == nil {
		return nil, fmt.Errorf("cannot update nil DeltaGeneration")
	}
	key := generationKeyOf(generation)
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
	result := s.getDB(ctx).Model(&model.DeltaGeneration{}).Where(
		"org_id = ? AND image_repository = ? AND source_digest = ? AND target_digest = ? AND resource_version = ?",
		key.OrgID, key.ImageRepository, key.SourceDigest, key.TargetDigest, expectedResourceVersion,
	).Updates(updates)
	if result.Error != nil {
		return nil, store.ErrorFromGormError(result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, flterrors.ErrNoRowsUpdated
	}
	return s.getGeneration(ctx, key)
}

func (s *DeltaStore) CreateDeltaPrepare(ctx context.Context, prep *model.DeltaPrepare) error {
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

func (s *DeltaStore) CreateOrReplaceWaitingDeltaPrepare(ctx context.Context, prep *model.DeltaPrepare) (PrepareAdmission, error) {
	if prep == nil {
		return PrepareAdmission{}, fmt.Errorf("cannot insert nil DeltaPrepare")
	}
	if prep.SourceResourceVersion <= 0 {
		return PrepareAdmission{}, fmt.Errorf("source resource version must be positive")
	}
	if prep.ID == uuid.Nil {
		prep.ID = uuid.New()
	}
	prep.Status = model.DeltaPrepareWaiting

	var admission PrepareAdmission
	err := store.RetryUpdate(func() (bool, error) {
		var err error
		admission, err = s.createOrReplaceWaitingDeltaPrepare(ctx, prep)
		return errors.Is(err, flterrors.ErrDuplicateName), err
	})
	return admission, err
}

func (s *DeltaStore) createOrReplaceWaitingDeltaPrepare(ctx context.Context, prep *model.DeltaPrepare) (PrepareAdmission, error) {
	var admission PrepareAdmission
	err := s.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		var latest model.DeltaPrepare
		result := tx.Where("org_id = ? AND kind = ? AND name = ?", prep.OrgID, prep.Kind, prep.Name).
			Order("source_resource_version DESC, created_at DESC, id DESC").
			Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&latest)
		if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return store.ErrorFromGormError(result.Error)
		}

		if result.Error == nil {
			switch {
			case prep.SourceResourceVersion < latest.SourceResourceVersion:
				admission = PrepareAdmission{Prepare: &latest}
				return nil
			case prep.SourceResourceVersion == latest.SourceResourceVersion:
				if !samePrepareIdentity(&latest, prep) {
					return fmt.Errorf("conflicting delta prepares have source resource version %d", prep.SourceResourceVersion)
				}
				admission = PrepareAdmission{Prepare: &latest, Accepted: latest.Status == model.DeltaPrepareWaiting}
				return nil
			}
		}

		result = tx.Model(&model.DeltaPrepare{}).
			Where("org_id = ? AND kind = ? AND name = ? AND status = ? AND source_resource_version < ?",
				prep.OrgID, prep.Kind, prep.Name, model.DeltaPrepareWaiting, prep.SourceResourceVersion).
			Updates(map[string]interface{}{
				"status":           model.DeltaPrepareFailed,
				"resource_version": gorm.Expr("resource_version + 1"),
			})
		if result.Error != nil {
			return store.ErrorFromGormError(result.Error)
		}
		admission.Replaced = result.RowsAffected > 0

		if err := tx.Create(prep).Error; err != nil {
			return store.ErrorFromGormError(err)
		}
		var created model.DeltaPrepare
		if err := tx.Where("id = ?", prep.ID).Take(&created).Error; err != nil {
			return store.ErrorFromGormError(err)
		}
		admission.Prepare = &created
		admission.Accepted = true
		return nil
	})
	return admission, err
}

func samePrepareIdentity(a, b *model.DeltaPrepare) bool {
	return equalStringPtr(a.TemplateVersion, b.TemplateVersion) && equalStringPtr(a.SpecHash, b.SpecHash)
}

func equalStringPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func (s *DeltaStore) GetDeltaPrepare(ctx context.Context, key PrepareKey, opts ...PrepareGetOption) (*model.DeltaPrepare, error) {
	cfg := &prepareGet{}
	for _, opt := range opts {
		opt(cfg)
	}
	q := s.getDB(ctx).Where("1 = 1")
	if key.ID != uuid.Nil {
		q = q.Where("id = ?", key.ID)
	}
	if key.OrgID != uuid.Nil {
		q = q.Where("org_id = ?", key.OrgID)
	}
	if key.Kind != "" {
		q = q.Where("kind = ?", key.Kind)
	}
	if key.Name != "" {
		q = q.Where("name = ?", key.Name)
	}
	if cfg.status != nil {
		q = q.Where("status = ?", *cfg.status)
	}
	if key.ID != uuid.Nil {
		var prep model.DeltaPrepare
		result := q.Take(&prep)
		if result.Error != nil {
			return nil, store.ErrorFromGormError(result.Error)
		}
		return &prep, nil
	}
	var prepares []model.DeltaPrepare
	result := q.Order("source_resource_version DESC, created_at DESC, id DESC").Limit(1).Find(&prepares)
	if result.Error != nil {
		return nil, store.ErrorFromGormError(result.Error)
	}
	if len(prepares) == 0 {
		return nil, nil
	}
	return &prepares[0], nil
}

func (s *DeltaStore) ListDeltaPrepares(ctx context.Context, ids []uuid.UUID) ([]model.DeltaPrepare, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var prepares []model.DeltaPrepare
	ids = lo.Uniq(ids)
	for start := 0; start < len(ids); start += deltaListBatchSize {
		end := min(start+deltaListBatchSize, len(ids))
		var batch []model.DeltaPrepare
		if err := s.getDB(ctx).Where("id IN ?", ids[start:end]).Find(&batch).Error; err != nil {
			return nil, store.ErrorFromGormError(err)
		}
		prepares = append(prepares, batch...)
	}
	return prepares, nil
}

func (s *DeltaStore) UpdateDeltaPrepare(ctx context.Context, expectedResourceVersion int64, prepare *model.DeltaPrepare) (*model.DeltaPrepare, error) {
	if prepare == nil {
		return nil, fmt.Errorf("cannot update nil DeltaPrepare")
	}
	updates := map[string]interface{}{
		"org_id":                  prepare.OrgID,
		"kind":                    prepare.Kind,
		"name":                    prepare.Name,
		"template_version":        prepare.TemplateVersion,
		"spec_hash":               prepare.SpecHash,
		"source_resource_version": prepare.SourceResourceVersion,
		"deadline":                prepare.Deadline,
		"status":                  prepare.Status,
		"resource_version":        gorm.Expr("resource_version + 1"),
	}
	result := s.getDB(ctx).Model(&model.DeltaPrepare{}).Where("id = ? AND resource_version = ?", prepare.ID, expectedResourceVersion).Updates(updates)
	if result.Error != nil {
		return nil, store.ErrorFromGormError(result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, flterrors.ErrNoRowsUpdated
	}
	return s.GetDeltaPrepare(ctx, PrepareKey{ID: prepare.ID})
}

func (s *DeltaStore) CreateDeltaPrepareGenerations(ctx context.Context, joins []*model.DeltaPrepareGeneration) ([]*model.DeltaPrepareGeneration, error) {
	if len(joins) == 0 {
		return nil, nil
	}
	for _, join := range joins {
		if join == nil {
			return nil, fmt.Errorf("cannot insert nil DeltaPrepareGeneration")
		}
	}

	inserted := make([]*model.DeltaPrepareGeneration, 0, len(joins))
	err := store.RunInTransaction(ctx, s.dbHandler, func(tx *gorm.DB) error {
		for _, join := range joins {
			result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(join)
			if result.Error != nil {
				return store.ErrorFromGormError(result.Error)
			}
			if result.RowsAffected == 1 {
				inserted = append(inserted, join)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return inserted, nil
}

func (s *DeltaStore) ListDeltaPrepareGenerations(ctx context.Context, filter DeltaPrepareGenerationListFilter) ([]model.DeltaPrepareGeneration, error) {
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
	for offset := 0; ; offset += deltaListBatchSize {
		pageSize := deltaListBatchSize
		var batch []model.DeltaPrepareGeneration
		result := query.Offset(offset).Limit(pageSize).Find(&batch)
		if result.Error != nil {
			return nil, store.ErrorFromGormError(result.Error)
		}
		rows = append(rows, batch...)
		if len(batch) < pageSize {
			break
		}
	}
	return rows, nil
}

func (s *DeltaStore) ListWaitingPastDeadline(ctx context.Context, limit int, asOf time.Time) ([]model.DeltaPrepare, error) {
	if limit < 1 || limit > MaxListWaitingPastDeadline {
		return nil, fmt.Errorf("limit must be between 1 and %d", MaxListWaitingPastDeadline)
	}
	if asOf.IsZero() {
		return nil, fmt.Errorf("asOf time is required")
	}
	var rows []model.DeltaPrepare
	result := s.getDB(ctx).Where(
		"status = ? AND deadline IS NOT NULL AND deadline < ?",
		model.DeltaPrepareWaiting, asOf,
	).Order("deadline ASC, id ASC").Limit(limit).Find(&rows)
	if result.Error != nil {
		return nil, store.ErrorFromGormError(result.Error)
	}
	return rows, nil
}

func (s *DeltaStore) countPreparePairs(ctx context.Context, prepareID uuid.UUID) (int, int, error) {
	var counts struct {
		Completed int `gorm:"column:completed"`
		Total     int `gorm:"column:total"`
	}
	result := s.getDB(ctx).Table("delta_prepare_generations AS pg").
		Select("COUNT(*) AS total, COUNT(*) FILTER (WHERE g.status IN (?, ?, ?)) AS completed", model.DeltaGenerationSucceeded, model.DeltaGenerationFailed, model.DeltaGenerationRejected).
		Joins("INNER JOIN delta_generations AS g ON g.org_id = pg.org_id AND g.image_repository = pg.image_repository AND g.source_digest = pg.source_digest AND g.target_digest = pg.target_digest").
		Where("pg.prepare_id = ?", prepareID).
		Scan(&counts)
	if result.Error != nil {
		return 0, 0, store.ErrorFromGormError(result.Error)
	}
	return counts.Completed, counts.Total, nil
}

func (s *DeltaStore) CountDeltaPrepareGenerations(ctx context.Context, prepareID uuid.UUID) (int, int, error) {
	return s.countPreparePairs(ctx, prepareID)
}
