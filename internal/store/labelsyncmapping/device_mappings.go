package labelsyncmapping

import (
	"context"
	"database/sql"
	"errors"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// DeviceMapping pairs a persisted mapping with its stable ownership identifier.
type DeviceMapping struct {
	ID      uuid.UUID
	Mapping domain.LabelSyncMapping
}

// DeviceMappingsSnapshot contains the current Device mappings and their revision.
type DeviceMappingsSnapshot struct {
	Mappings []DeviceMapping
	Revision int64
}

// GetDeviceMappingsSnapshot reads mappings and the revision from one
// repeatable-read snapshot. Callers must pass a context without an existing
// store transaction; otherwise the nested transaction will not establish the
// requested isolation level.
func (s *labelSyncMappingStore) GetDeviceMappingsSnapshot(ctx context.Context, orgID uuid.UUID) (DeviceMappingsSnapshot, error) {
	snapshot := DeviceMappingsSnapshot{Mappings: []DeviceMapping{}}
	err := store.DB(ctx, s.db).Transaction(func(tx *gorm.DB) error {
		var mappings []model.LabelSyncMapping
		if err := tx.Where("org_id = ? AND spec IS NOT NULL AND spec->>'resourceType' = ?", orgID, domain.LabelSyncMappingDevice).
			Order("name ASC").Find(&mappings).Error; err != nil {
			return store.ErrorFromGormError(err)
		}
		snapshot.Mappings = make([]DeviceMapping, 0, len(mappings))
		for i := range mappings {
			mapping, err := mappings[i].ToApiResource()
			if err != nil {
				return err
			}
			snapshot.Mappings = append(snapshot.Mappings, DeviceMapping{ID: mappings[i].ID, Mapping: *mapping})
		}

		var state model.LabelSyncState
		err := tx.Where("org_id = ? AND resource_type = ?", orgID, domain.LabelSyncMappingDevice).Take(&state).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return store.ErrorFromGormError(err)
		}
		snapshot.Revision = state.Revision
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return DeviceMappingsSnapshot{}, err
	}
	return snapshot, nil
}
