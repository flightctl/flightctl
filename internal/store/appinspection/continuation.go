package appinspection

import (
	"context"
	"errors"
	"time"

	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/storeutil"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const freshContinuation = `NOT EXISTS (
	SELECT 1 FROM application_inspection_usages AS u JOIN application_inspections AS a ON a.org_id = u.org_id AND a.id = u.inspection_id
	WHERE u.org_id = application_inspection_continuations.org_id AND u.continuation_id = application_inspection_continuations.id
	AND a.fresh_until <= clock_timestamp()
)`

type OwnerProgress struct {
	model.ApplicationInspectionContinuation
	NeedsRetry bool
}

func (s *Store) CurrentContinuation(ctx context.Context, orgID uuid.UUID, owner Owner) (OwnerProgress, error) {
	_, column, err := owner.tableAndColumn()
	if err != nil {
		return OwnerProgress{}, err
	}
	var progress OwnerProgress
	err = forOwner(store.DB(ctx, s.db).Model(&model.ApplicationInspectionContinuation{}), column, orgID, owner.Name).
		Select(`application_inspection_continuations.*, EXISTS (
			SELECT 1 FROM application_inspection_usages AS u JOIN application_inspections AS a ON a.org_id = u.org_id AND a.id = u.inspection_id
			WHERE u.org_id = application_inspection_continuations.org_id AND u.continuation_id = application_inspection_continuations.id
			AND (u.pending OR a.state = 'finished_incomplete')
		) AS needs_retry`).Where("current").Take(&progress).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = nil
	}
	return progress, storeutil.ErrorFromGormError(err)
}

type ReadyContinuation struct {
	model.ApplicationInspectionContinuation
	Fresh bool
}

func (s *Store) ReadyContinuation(ctx context.Context, orgID, id uuid.UUID) (ReadyContinuation, error) {
	var progress ReadyContinuation
	err := store.DB(ctx, s.db).Model(&model.ApplicationInspectionContinuation{}).
		Select("application_inspection_continuations.*, "+freshContinuation+" AS fresh").
		Where("org_id = ? AND id = ?", orgID, id).Where(runnableContinuation).Take(&progress).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = nil
	}
	return progress, storeutil.ErrorFromGormError(err)
}

func (s *Store) DiscardContinuation(ctx context.Context, orgID, id uuid.UUID) error {
	return storeutil.ErrorFromGormError(store.DB(ctx, s.db).Model(&model.ApplicationInspectionContinuation{}).
		Where("org_id = ? AND id = ? AND current", orgID, id).Where(readyContinuation).
		Updates(map[string]any{"current": false, "superseded_at": gorm.Expr("clock_timestamp()")}).Error)
}

// RecordPublication belongs to the same transaction as the rendered write.
// The recorded version remains runnable until Redis has been notified.
func (s *Store) RecordPublication(ctx context.Context, orgID, id uuid.UUID, version string) error {
	db := store.DB(ctx, s.db)
	return storeutil.ErrorFromGormError(db.Model(&model.ApplicationInspectionContinuation{}).
		Where("org_id = ? AND id = ? AND consumed_at IS NOT NULL", orgID, id).Update("pending_version", version).Error)
}

func (s *Store) AcknowledgePublication(ctx context.Context, orgID, id uuid.UUID, version string) error {
	db := store.DB(ctx, s.db)
	return storeutil.ErrorFromGormError(db.Model(&model.ApplicationInspectionContinuation{}).
		Where("org_id = ? AND id = ? AND pending_version = ?", orgID, id, version).Update("pending_version", nil).Error)
}

func (s *Store) DeleteSupersededContinuations(ctx context.Context, orgID uuid.UUID, cutoff time.Time, limit int) (int64, error) {
	if err := validatePageSize(limit); err != nil {
		return 0, err
	}
	db := store.DB(ctx, s.db)
	ids := db.Model(&model.ApplicationInspectionContinuation{}).Select("id").
		Where("org_id = ? AND superseded_at < ? AND NOT current AND pending_version IS NULL", orgID, cutoff).Order("superseded_at, id").Limit(limit)
	result := db.Where("org_id = ? AND id IN (?)", orgID, ids).Delete(&model.ApplicationInspectionContinuation{})
	return result.RowsAffected, storeutil.ErrorFromGormError(result.Error)
}

type WorkTarget struct {
	Continuation       model.ApplicationInspectionContinuation `gorm:"embedded"`
	ApplicationContext string
}

// WorkTarget finds a current waiter from which to reload execution inputs.
func (s *Store) WorkTarget(ctx context.Context, orgID, id uuid.UUID) (WorkTarget, error) {
	var target WorkTarget
	err := store.DB(ctx, s.db).Table("application_inspection_continuations AS c").
		Select("c.*, u.application_context").
		Joins("JOIN application_inspection_usages AS u ON u.org_id = c.org_id AND u.continuation_id = c.id").
		Where("u.org_id = ? AND u.inspection_id = ? AND u.pending AND c.current", orgID, id).
		Order("c.id").Take(&target).Error
	return target, storeutil.ErrorFromGormError(err)
}

// InvalidateWaiter detaches stale work without settling the other owners of a
// shared attempt. The stale continuation becomes runnable for reconciliation.
func (s *Store) InvalidateWaiter(ctx context.Context, orgID, id, token, continuationID uuid.UUID) (bool, error) {
	var applied bool
	err := store.DB(ctx, s.db).Transaction(func(tx *gorm.DB) error {
		_, err := lockClaim(tx, orgID, id, token)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		result := tx.Model(&model.ApplicationInspectionContinuation{}).Where("org_id = ? AND id = ? AND current", orgID, continuationID).Update("invalidated", true)
		if result.Error != nil {
			return result.Error
		}
		applied = result.RowsAffected != 0
		if applied {
			if err := tx.Model(&model.ApplicationInspectionUsage{}).Where("org_id = ? AND continuation_id = ? AND pending", orgID, continuationID).Update("pending", false).Error; err != nil {
				return err
			}
		}
		return tx.Model(&model.ApplicationInspection{}).Where("org_id = ? AND id = ?", orgID, id).
			Updates(map[string]any{"claim_token": nil, "claim_until": nil}).Error
	})
	return applied, storeutil.ErrorFromGormError(err)
}

func (s *Store) ListReadyForInspection(ctx context.Context, orgID, id, after uuid.UUID, limit int) ([]uuid.UUID, error) {
	return s.listWorkIDs(ctx, &model.ApplicationInspectionContinuation{}, orgID, after, limit, readyContinuation+` AND EXISTS (
		SELECT 1 FROM application_inspection_usages AS u
		WHERE u.org_id = application_inspection_continuations.org_id AND u.continuation_id = application_inspection_continuations.id AND u.inspection_id = ?
	)`, id)
}
