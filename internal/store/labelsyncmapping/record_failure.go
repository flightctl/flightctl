package labelsyncmapping

import (
	"context"
	"errors"
	"strconv"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *labelSyncMappingStore) RecordReconciliationFailure(ctx context.Context, orgID uuid.UUID, failure ReconciliationFailure) (bool, error) {
	resourceVersion, err := strconv.ParseInt(failure.ResourceVersion, 10, 64)
	if err != nil {
		return false, flterrors.ErrIllegalResourceVersionFormat
	}

	updated := false
	err = store.RunInTransaction(ctx, s.db, func(tx *gorm.DB) error {
		var current model.LabelSyncMapping
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("org_id = ? AND id = ? AND generation = ? AND resource_version = ? AND deletion_timestamp IS NULL",
				orgID, failure.MappingID, failure.Generation, resourceVersion).
			Take(&current).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return store.ErrorFromGormError(err)
		}

		status := domain.LabelSyncMappingStatus{Conditions: &[]domain.Condition{}}
		if current.Status != nil {
			status = current.Status.Data
			if status.Conditions == nil {
				status.Conditions = &[]domain.Condition{}
			}
		}
		domain.SetStatusCondition(status.Conditions, domain.Condition{
			Type:               domain.ConditionTypeLabelSyncMappingReady,
			Status:             domain.ConditionStatusFalse,
			Reason:             "Degraded",
			Message:            failure.Message,
			ObservedGeneration: lo.ToPtr(failure.Generation),
		})

		result := tx.Model(&model.LabelSyncMapping{}).
			Where("org_id = ? AND id = ? AND generation = ? AND resource_version = ? AND deletion_timestamp IS NULL",
				orgID, failure.MappingID, failure.Generation, resourceVersion).
			Updates(map[string]interface{}{
				"status":           model.MakeJSONField(status),
				"failure_revision": gorm.Expr("failure_revision + 1"),
			})
		if result.Error != nil {
			return store.ErrorFromGormError(result.Error)
		}
		updated = result.RowsAffected == 1
		return nil
	})
	if err != nil {
		return false, store.ErrorFromGormError(err)
	}
	return updated, nil
}
