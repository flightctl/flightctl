package deltaprepare

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	deltagenerationstore "github.com/flightctl/flightctl/internal/delta_worker/store/deltageneration"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const MaxListWaitingPastDeadline = 1000

const (
	prepareListBatchSize        = 500
	waitingPrepareIndex         = "idx_delta_prepares_one_waiting"
	waitingPrepareDeadlineIndex = "idx_delta_prepares_waiting_deadline"
)

// Store is the resource-oriented persistence API used by the
// prepare service. Updates replace the complete object under CAS.
type Store interface {
	CreateDeltaPrepare(ctx context.Context, prepare *model.DeltaPrepare) error
	CreateOrReplaceWaitingDeltaPrepare(ctx context.Context, prepare *model.DeltaPrepare) (PrepareAdmission, error)
	GetDeltaPrepare(ctx context.Context, key PrepareKey, opts ...PrepareGetOption) (*model.DeltaPrepare, error)
	ListDeltaPrepares(ctx context.Context, ids []uuid.UUID) ([]model.DeltaPrepare, error)
	UpdateDeltaPrepare(ctx context.Context, expectedResourceVersion int64, prepare *model.DeltaPrepare) (*model.DeltaPrepare, error)
	CountDeltaPrepareGenerations(ctx context.Context, prepareID uuid.UUID) (completed, total int, err error)
	// DecrementPendingGenerationsForGeneration atomically advances every waiting
	// prepare joined to a currently terminal generation. Join-level completion
	// markers prevent redelivered notifications from decrementing again. The
	// returned progress includes the grouped completed/total counts needed to
	// update the owning resource without one count query per prepare.
	DecrementPendingGenerationsForGeneration(ctx context.Context, key deltagenerationstore.GenerationKey) ([]PrepareProgress, error)
}

var _ Store = (*PrepareStore)(nil)

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

// PrepareProgress is the state of a prepare after one terminal generation has
// been applied to its pending-generation counter.
type PrepareProgress struct {
	Prepare   model.DeltaPrepare
	Completed int
	Total     int
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

// PrepareStore persists delta prepare resources and their completion counters.
type PrepareStore struct {
	dbHandler *gorm.DB
	log       logrus.FieldLogger
}

// NewStore creates a store for delta prepares.
func NewStore(db *gorm.DB, log logrus.FieldLogger) *PrepareStore {
	return &PrepareStore{dbHandler: db, log: log}
}

func (s *PrepareStore) getDB(ctx context.Context) *gorm.DB {
	return s.dbHandler.WithContext(ctx)
}

// InitialMigration creates the delta prepare resource table and its resource-specific indexes.
func (s *PrepareStore) InitialMigration(ctx context.Context) error {
	db := s.getDB(ctx)
	if err := db.AutoMigrate(&model.DeltaPrepare{}); err != nil {
		return err
	}
	if db.Dialector.Name() != "postgres" {
		return nil
	}
	if !db.Migrator().HasIndex(&model.DeltaPrepare{}, waitingPrepareIndex) {
		if err := db.Exec(`
		CREATE UNIQUE INDEX IF NOT EXISTS idx_delta_prepares_one_waiting
		ON delta_prepares (org_id, kind, name)
		WHERE status = 'waiting'
		`).Error; err != nil {
			return err
		}
	}
	if db.Migrator().HasIndex(&model.DeltaPrepare{}, waitingPrepareDeadlineIndex) {
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

func (s *PrepareStore) CreateDeltaPrepare(ctx context.Context, prep *model.DeltaPrepare) error {
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

func (s *PrepareStore) CreateOrReplaceWaitingDeltaPrepare(ctx context.Context, prep *model.DeltaPrepare) (PrepareAdmission, error) {
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

func (s *PrepareStore) createOrReplaceWaitingDeltaPrepare(ctx context.Context, prep *model.DeltaPrepare) (PrepareAdmission, error) {
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

func (s *PrepareStore) GetDeltaPrepare(ctx context.Context, key PrepareKey, opts ...PrepareGetOption) (*model.DeltaPrepare, error) {
	cfg := &prepareGet{}
	for _, opt := range opts {
		opt(cfg)
	}
	if key.ID == uuid.Nil {
		if key.OrgID == uuid.Nil || key.Kind == "" || key.Name == "" {
			return nil, fmt.Errorf("prepare key requires either ID or org, kind and name")
		}
	} else if key.OrgID != uuid.Nil || key.Kind != "" || key.Name != "" {
		return nil, fmt.Errorf("prepare key requires either ID or org, kind and name")
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

func (s *PrepareStore) ListDeltaPrepares(ctx context.Context, ids []uuid.UUID) ([]model.DeltaPrepare, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var prepares []model.DeltaPrepare
	ids = lo.Uniq(ids)
	for start := 0; start < len(ids); start += prepareListBatchSize {
		end := min(start+prepareListBatchSize, len(ids))
		var batch []model.DeltaPrepare
		if err := s.getDB(ctx).Where("id IN ?", ids[start:end]).Find(&batch).Error; err != nil {
			return nil, store.ErrorFromGormError(err)
		}
		prepares = append(prepares, batch...)
	}
	return prepares, nil
}

func (s *PrepareStore) UpdateDeltaPrepare(ctx context.Context, expectedResourceVersion int64, prepare *model.DeltaPrepare) (*model.DeltaPrepare, error) {
	if prepare == nil {
		return nil, fmt.Errorf("cannot update nil DeltaPrepare")
	}
	updates := map[string]interface{}{
		"org_id":                    prepare.OrgID,
		"kind":                      prepare.Kind,
		"name":                      prepare.Name,
		"template_version":          prepare.TemplateVersion,
		"spec_hash":                 prepare.SpecHash,
		"source_resource_version":   prepare.SourceResourceVersion,
		"deadline":                  prepare.Deadline,
		"pending_generations_count": prepare.PendingGenerationsCount,
		"status":                    prepare.Status,
		"resource_version":          gorm.Expr("resource_version + 1"),
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

func (s *PrepareStore) CountDeltaPrepareGenerations(ctx context.Context, prepareID uuid.UUID) (int, int, error) {
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

func (s *PrepareStore) DecrementPendingGenerationsForGeneration(ctx context.Context, key deltagenerationstore.GenerationKey) ([]PrepareProgress, error) {
	var progress []PrepareProgress
	err := store.RunInTransaction(ctx, s.dbHandler, func(tx *gorm.DB) error {
		// Mark the generation/prepare join before decrementing the denormalized
		// counter. This makes duplicate or redelivered completion events
		// idempotent: only the transaction that flips completed from false to
		// true receives a prepare ID to decrement.
		var completedJoins []model.DeltaPrepareGeneration
		result := tx.Model(&completedJoins).
			Where(`
				org_id = @org_id
				AND image_repository = @image_repository
				AND source_digest = @source_digest
				AND target_digest = @target_digest
				AND completed = @not_completed
				AND EXISTS (
					SELECT 1
					FROM delta_generations AS g
					WHERE g.org_id = @org_id
					  AND g.image_repository = @image_repository
					  AND g.source_digest = @source_digest
					  AND g.target_digest = @target_digest
					  AND g.status IN @terminal_statuses
				)
				AND EXISTS (
					SELECT 1
					FROM delta_prepares AS p
					WHERE p.id = prepare_id
					  AND p.status = @waiting
					  AND p.pending_generations_count > @minimum_pending
				)
			`, map[string]interface{}{
				"org_id":            key.OrgID,
				"image_repository":  key.ImageRepository,
				"source_digest":     key.SourceDigest,
				"target_digest":     key.TargetDigest,
				"not_completed":     false,
				"terminal_statuses": []string{model.DeltaGenerationSucceeded, model.DeltaGenerationFailed, model.DeltaGenerationRejected},
				"waiting":           model.DeltaPrepareWaiting,
				"minimum_pending":   0,
			}).
			Clauses(clause.Returning{Columns: []clause.Column{{Name: "prepare_id"}}}).
			Updates(map[string]interface{}{"completed": true})
		if result.Error != nil {
			return store.ErrorFromGormError(result.Error)
		}
		newlyClaimedPrepareIDs := make([]uuid.UUID, 0, len(completedJoins))
		for _, join := range completedJoins {
			newlyClaimedPrepareIDs = append(newlyClaimedPrepareIDs, join.PrepareID)
		}
		if len(newlyClaimedPrepareIDs) > 0 {
			var prepares []model.DeltaPrepare
			result = tx.Model(&prepares).
				Where(`
					status = @waiting
					AND pending_generations_count > @minimum_pending
				`, map[string]interface{}{
					"waiting":         model.DeltaPrepareWaiting,
					"minimum_pending": 0,
				}).
				Where("id IN @prepare_ids", map[string]interface{}{"prepare_ids": newlyClaimedPrepareIDs}).
				Clauses(clause.Returning{}).
				Updates(map[string]interface{}{
					"pending_generations_count": gorm.Expr("pending_generations_count - 1"),
					"status": clause.NamedExpr{
						SQL: "CASE WHEN pending_generations_count = @last_pending THEN @complete ELSE status END",
						Vars: []interface{}{map[string]interface{}{
							"last_pending": 1,
							"complete":     model.DeltaPrepareComplete,
						}},
					},
					"resource_version": gorm.Expr("resource_version + 1"),
				})
			if result.Error != nil {
				return store.ErrorFromGormError(result.Error)
			}

			counts, err := countPrepareProgress(tx, newlyClaimedPrepareIDs)
			if err != nil {
				return err
			}
			for _, prepare := range prepares {
				count, ok := counts[prepare.ID]
				if !ok {
					return fmt.Errorf("missing generation counts for prepare %s", prepare.ID)
				}
				progress = append(progress, PrepareProgress{
					Prepare:   prepare,
					Completed: count.Completed,
					Total:     count.Total,
				})
			}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}
	return progress, nil
}

type prepareProgressCount struct {
	PrepareID uuid.UUID `gorm:"column:prepare_id"`
	Completed int       `gorm:"column:completed"`
	Total     int       `gorm:"column:total"`
}

func countPrepareProgress(tx *gorm.DB, prepareIDs []uuid.UUID) (map[uuid.UUID]prepareProgressCount, error) {
	var rows []prepareProgressCount
	result := tx.Table("delta_prepare_generations AS pg").
		Select("pg.prepare_id, COUNT(*) AS total, COUNT(*) FILTER (WHERE g.status IN @terminal_statuses) AS completed", map[string]interface{}{
			"terminal_statuses": []string{model.DeltaGenerationSucceeded, model.DeltaGenerationFailed, model.DeltaGenerationRejected},
		}).
		Joins("INNER JOIN delta_generations AS g ON g.org_id = pg.org_id AND g.image_repository = pg.image_repository AND g.source_digest = pg.source_digest AND g.target_digest = pg.target_digest").
		Where("pg.prepare_id IN @prepare_ids", map[string]interface{}{"prepare_ids": prepareIDs}).
		Group("pg.prepare_id").
		Scan(&rows)
	if result.Error != nil {
		return nil, store.ErrorFromGormError(result.Error)
	}
	counts := make(map[uuid.UUID]prepareProgressCount, len(rows))
	for _, row := range rows {
		counts[row.PrepareID] = row
	}
	return counts, nil
}

func (s *PrepareStore) ListWaitingPastDeadline(ctx context.Context, limit int, asOf time.Time) ([]model.DeltaPrepare, error) {
	if limit < 1 || limit > MaxListWaitingPastDeadline {
		return nil, fmt.Errorf("limit must be between 1 and %d", MaxListWaitingPastDeadline)
	}
	if asOf.IsZero() {
		return nil, fmt.Errorf("asOf time is required")
	}
	var rows []model.DeltaPrepare
	result := s.getDB(ctx).Where(
		"status IN ? AND deadline IS NOT NULL AND deadline < ?",
		[]string{model.DeltaPrepareWaiting, model.DeltaPrepareFailing}, asOf,
	).Order("deadline ASC, id ASC").Limit(limit).Find(&rows)
	if result.Error != nil {
		return nil, store.ErrorFromGormError(result.Error)
	}
	return rows, nil
}
