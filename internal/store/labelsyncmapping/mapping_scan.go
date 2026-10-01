package labelsyncmapping

import (
	"context"
	"errors"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"gorm.io/gorm"
)

type MappingScanRecord struct {
	MappingID        uuid.UUID
	Generation       int64
	DeletionRevision *int64
	FailureRevision  int64
}

func (s *labelSyncMappingStore) ListMappingScanTargets(ctx context.Context, orgID uuid.UUID) ([]MappingScanRecord, error) {
	var mappings []model.LabelSyncMapping
	if err := store.DB(ctx, s.db).Where("org_id = ? AND spec IS NOT NULL AND spec->>'resourceType' = ?", orgID, domain.LabelSyncMappingDevice).
		Order("name ASC").Find(&mappings).Error; err != nil {
		return nil, store.ErrorFromGormError(err)
	}

	targets := make([]MappingScanRecord, 0, len(mappings))
	for _, mapping := range mappings {
		if !mappingNeedsScan(mapping) {
			continue
		}
		targets = append(targets, MappingScanRecord{
			MappingID:        mapping.ID,
			Generation:       lo.FromPtr(mapping.Generation),
			DeletionRevision: cloneInt64(mapping.DeletionRevision),
			FailureRevision:  mapping.FailureRevision,
		})
	}
	return targets, nil
}

func mappingNeedsScan(mapping model.LabelSyncMapping) bool {
	if mapping.DeletionTimestamp != nil {
		return true
	}
	if mapping.Status == nil || mapping.Status.Data.Conditions == nil {
		return false
	}
	ready := domain.FindStatusCondition(*mapping.Status.Data.Conditions, domain.ConditionTypeLabelSyncMappingReady)
	return ready != nil && ready.Status == domain.ConditionStatusFalse && (ready.Reason == "Pending" || ready.Reason == "Degraded")
}

func (s *labelSyncMappingStore) RecordMappingScanFailure(ctx context.Context, orgID uuid.UUID, token MappingScanRecord, message string) (MappingScanRecord, bool, error) {
	updatedToken := token
	updatedToken.DeletionRevision = cloneInt64(token.DeletionRevision)
	updated := false
	err := store.RunInTransaction(ctx, s.db, func(tx *gorm.DB) error {
		status := domain.LabelSyncMappingStatus{Conditions: &[]domain.Condition{}}
		var current model.LabelSyncMapping
		query := tx.Where("org_id = ? AND id = ? AND generation = ? AND failure_revision = ?", orgID, token.MappingID, token.Generation, token.FailureRevision)
		query = withDeletionRevision(query, token.DeletionRevision)
		if err := query.Take(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return store.ErrorFromGormError(err)
		}
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
			Message:            message,
			ObservedGeneration: lo.ToPtr(token.Generation),
		})

		write := tx.Model(&model.LabelSyncMapping{}).
			Where("org_id = ? AND id = ? AND generation = ? AND failure_revision = ?", orgID, token.MappingID, token.Generation, token.FailureRevision)
		write = withDeletionRevision(write, token.DeletionRevision)
		result := write.Updates(map[string]interface{}{
			"status":           model.MakeJSONField(status),
			"failure_revision": gorm.Expr("failure_revision + 1"),
		})
		if result.Error != nil {
			return store.ErrorFromGormError(result.Error)
		}
		updated = result.RowsAffected == 1
		if updated {
			updatedToken.FailureRevision++
		}
		return nil
	})
	return updatedToken, updated, err
}

func (s *labelSyncMappingStore) CompleteMappingScan(ctx context.Context, orgID uuid.UUID, targets []MappingScanRecord) (map[uuid.UUID]bool, error) {
	completed := make(map[uuid.UUID]bool, len(targets))
	for _, target := range targets {
		completed[target.MappingID] = false
	}
	if len(targets) == 0 {
		return completed, nil
	}

	err := store.RunInTransaction(ctx, s.db, func(tx *gorm.DB) error {
		for _, target := range targets {
			var current model.LabelSyncMapping
			query := tx.Where("org_id = ? AND id = ? AND generation = ? AND failure_revision = ? AND spec->>'resourceType' = ?",
				orgID, target.MappingID, target.Generation, target.FailureRevision, domain.LabelSyncMappingDevice)
			query = withDeletionRevision(query, target.DeletionRevision)
			if err := query.Take(&current).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					continue
				}
				return store.ErrorFromGormError(err)
			}

			if current.DeletionTimestamp != nil {
				const savepoint = "label_mapping_scan_finalize"
				if err := tx.SavePoint(savepoint).Error; err != nil {
					return err
				}
				delete := tx.Unscoped().Where("org_id = ? AND id = ? AND generation = ? AND failure_revision = ? AND deletion_timestamp IS NOT NULL",
					orgID, target.MappingID, target.Generation, target.FailureRevision)
				delete = withDeletionRevision(delete, target.DeletionRevision)
				result := delete.Delete(&model.LabelSyncMapping{})
				if errors.Is(result.Error, gorm.ErrForeignKeyViolated) {
					if err := tx.RollbackTo(savepoint).Error; err != nil {
						return err
					}
					continue
				}
				if result.Error != nil {
					return store.ErrorFromGormError(result.Error)
				}
				if result.RowsAffected == 1 {
					if err := s.incrementRevision(tx, orgID, domain.LabelSyncMappingDevice); err != nil {
						return err
					}
					completed[target.MappingID] = true
				}
				continue
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
				Status:             domain.ConditionStatusTrue,
				Reason:             "Success",
				Message:            "Mapping propagation is complete",
				ObservedGeneration: lo.ToPtr(target.Generation),
			})
			write := tx.Model(&model.LabelSyncMapping{}).
				Where("org_id = ? AND id = ? AND generation = ? AND failure_revision = ? AND deletion_timestamp IS NULL",
					orgID, target.MappingID, target.Generation, target.FailureRevision)
			write = withDeletionRevision(write, target.DeletionRevision)
			result := write.Update("status", model.MakeJSONField(status))
			if result.Error != nil {
				return store.ErrorFromGormError(result.Error)
			}
			completed[target.MappingID] = result.RowsAffected == 1
		}
		return nil
	})
	return completed, err
}

func withDeletionRevision(query *gorm.DB, revision *int64) *gorm.DB {
	if revision == nil {
		return query.Where("deletion_revision IS NULL")
	}
	return query.Where("deletion_revision = ?", *revision)
}
