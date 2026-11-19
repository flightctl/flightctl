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
	"gorm.io/gorm/clause"
)

const inspectionWithoutUsage = `NOT EXISTS (
	SELECT 1 FROM application_inspection_usages AS usage
	WHERE usage.org_id = application_inspections.org_id AND usage.inspection_id = application_inspections.id
)`

// DeleteExpiredUnreferenced reclaims one bounded batch of unused attempts and their refs.
func (s *Store) DeleteExpiredUnreferenced(ctx context.Context, orgID uuid.UUID, cutoff time.Time, limit int) (int64, error) {
	if limit <= 0 {
		return 0, errors.New("inspection cleanup requires a positive batch limit")
	}
	var deleted int64
	err := store.RunInTransaction(ctx, s.db, func(tx *gorm.DB) error {
		var ids []uuid.UUID
		if err := tx.Model(&model.ApplicationInspection{}).Where("org_id = ? AND created_at < ?", orgID, cutoff).
			Where(inspectionWithoutUsage).Order("created_at, id").Limit(limit).
			Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		// Recheck under the candidate locks with a fresh snapshot in case a usage
		// committed after the first query read the usage table.
		result := tx.Where("org_id = ? AND created_at < ? AND id IN ?", orgID, cutoff, ids).
			Where(inspectionWithoutUsage).Delete(&model.ApplicationInspection{})
		deleted = result.RowsAffected
		return result.Error
	})
	if err != nil {
		return 0, storeutil.ErrorFromGormError(err)
	}
	return deleted, nil
}
