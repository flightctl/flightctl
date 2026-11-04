package labelsyncmapping

import (
	"context"
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

// GetDeviceMappingsRevision reads the current Device mapping revision. An
// organization without a state row has revision zero.
func (s *labelSyncMappingStore) GetDeviceMappingsRevision(ctx context.Context, orgID uuid.UUID) (int64, error) {
	return getDeviceMappingsRevision(store.DB(ctx, s.db), orgID)
}

func getDeviceMappingsRevision(db *gorm.DB, orgID uuid.UUID) (int64, error) {
	var state model.LabelSyncState
	err := db.Select("revision").
		Where("org_id = ? AND resource_type = ?", orgID, domain.LabelSyncMappingDevice).Take(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, store.ErrorFromGormError(err)
	}
	return state.Revision, nil
}

// GetDeviceMappingsSnapshot reads mappings and their revision from one SQL
// statement snapshot, including when the caller owns a transaction.
func (s *labelSyncMappingStore) GetDeviceMappingsSnapshot(ctx context.Context, orgID uuid.UUID) (DeviceMappingsSnapshot, error) {
	var rows []struct {
		model.LabelSyncMapping
		SnapshotRevision int64
	}
	// The singleton revision row survives the left join when no mappings match.
	err := store.DB(ctx, s.db).Raw(`
		SELECT mappings.*, state.revision AS snapshot_revision
		FROM (
			SELECT COALESCE((SELECT revision FROM label_sync_state WHERE org_id = ? AND resource_type = ?), 0) AS revision
		) AS state
		LEFT JOIN label_sync_mappings AS mappings
			ON mappings.org_id = ? AND mappings.spec IS NOT NULL
			AND mappings.spec->>'resourceType' = ? AND mappings.deleted_at IS NULL
		ORDER BY mappings.name ASC`, orgID, domain.LabelSyncMappingDevice, orgID, domain.LabelSyncMappingDevice).Scan(&rows).Error
	if err != nil {
		return DeviceMappingsSnapshot{}, store.ErrorFromGormError(err)
	}
	snapshot := DeviceMappingsSnapshot{Mappings: make([]DeviceMapping, 0, len(rows))}
	for i := range rows {
		snapshot.Revision = rows[i].SnapshotRevision
		if rows[i].ID == uuid.Nil {
			continue
		}
		mapping, err := rows[i].ToApiResource()
		if err != nil {
			return DeviceMappingsSnapshot{}, err
		}
		snapshot.Mappings = append(snapshot.Mappings, DeviceMapping{ID: rows[i].ID, Mapping: *mapping})
	}
	return snapshot, nil
}
