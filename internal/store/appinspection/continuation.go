package appinspection

import (
	"context"
	"errors"

	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/storeutil"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

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
