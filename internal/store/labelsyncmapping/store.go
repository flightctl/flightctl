package labelsyncmapping

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const destinationKeyUniqueIndexName = "label_sync_mappings_org_resource_destination_key_uq"

type Store interface {
	InitialMigration(context.Context) error
	Create(context.Context, uuid.UUID, *domain.LabelSyncMapping) (*domain.LabelSyncMapping, error)
	Update(context.Context, uuid.UUID, *domain.LabelSyncMapping) (*domain.LabelSyncMapping, *domain.LabelSyncMapping, error)
	Get(context.Context, uuid.UUID, string) (*domain.LabelSyncMapping, error)
	List(context.Context, uuid.UUID, store.ListParams) (*domain.LabelSyncMappingList, error)
	Delete(context.Context, uuid.UUID, string) (bool, error)
	FinalizeDelete(context.Context, uuid.UUID, string) (bool, error)
	GetDeviceMappingsSnapshot(context.Context, uuid.UUID) (DeviceMappingsSnapshot, error)
	ListMappingScanTargets(context.Context, uuid.UUID) ([]MappingScanRecord, error)
	RecordMappingScanFailure(context.Context, uuid.UUID, MappingScanRecord, string) (MappingScanRecord, bool, error)
	CompleteMappingScan(context.Context, uuid.UUID, []MappingScanRecord) (map[uuid.UUID]bool, error)
}

type labelSyncMappingStore struct {
	db           *gorm.DB
	genericStore *store.GenericStore[*model.LabelSyncMapping, model.LabelSyncMapping, domain.LabelSyncMapping, domain.LabelSyncMappingList]
}

func NewStore(db *gorm.DB, log logrus.FieldLogger) Store {
	return &labelSyncMappingStore{
		db: db,
		genericStore: store.NewGenericStore[*model.LabelSyncMapping, model.LabelSyncMapping, domain.LabelSyncMapping, domain.LabelSyncMappingList](
			db, log, model.NewLabelSyncMappingFromApiResource, (*model.LabelSyncMapping).ToApiResource, model.LabelSyncMappingsToApiResource),
	}
}

func (s *labelSyncMappingStore) InitialMigration(ctx context.Context) error {
	db := s.db.WithContext(ctx)
	if err := db.AutoMigrate(&model.LabelSyncMapping{}, &model.LabelSyncState{}); err != nil {
		return err
	}
	if db.Dialector.Name() != "postgres" {
		return nil
	}

	indexes := []string{
		`CREATE UNIQUE INDEX label_sync_mappings_org_id_id_uq ON label_sync_mappings (org_id, id)`,
		`CREATE UNIQUE INDEX ` + destinationKeyUniqueIndexName + ` ON label_sync_mappings (org_id, (spec->>'resourceType'), (spec->>'key')) WHERE spec->>'key' IS NOT NULL`,
		`CREATE INDEX device_labels_label_sync_mapping_idx ON device_labels (org_id, label_sync_mapping_id) WHERE label_sync_mapping_id IS NOT NULL`,
		`CREATE INDEX device_labels_label_sync_key_idx ON device_labels (org_id, label_key) INCLUDE (label_sync_mapping_id)`,
	}
	models := []any{
		&model.LabelSyncMapping{},
		&model.LabelSyncMapping{},
		&model.DeviceLabel{},
		&model.DeviceLabel{},
	}
	names := []string{
		"label_sync_mappings_org_id_id_uq",
		destinationKeyUniqueIndexName,
		"device_labels_label_sync_mapping_idx",
		"device_labels_label_sync_key_idx",
	}
	for i, statement := range indexes {
		if !db.Migrator().HasIndex(models[i], names[i]) {
			if err := db.Exec(statement).Error; err != nil {
				return err
			}
		}
	}

	if !db.Migrator().HasConstraint(&model.DeviceLabel{}, "device_labels_label_sync_mapping_fk") {
		if err := db.Exec(`ALTER TABLE device_labels
			ADD CONSTRAINT device_labels_label_sync_mapping_fk
			FOREIGN KEY (org_id, label_sync_mapping_id)
			REFERENCES label_sync_mappings (org_id, id) ON DELETE RESTRICT`).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *labelSyncMappingStore) Create(ctx context.Context, orgID uuid.UUID, mapping *domain.LabelSyncMapping) (*domain.LabelSyncMapping, error) {
	modelMapping, err := model.NewLabelSyncMappingFromApiResource(mapping)
	if err != nil {
		return nil, err
	}
	modelMapping.OrgID = orgID
	modelMapping.ID = uuid.New()
	modelMapping.Generation = lo.ToPtr(int64(1))
	modelMapping.ResourceVersion = lo.ToPtr(int64(1))

	err = store.RunInTransaction(ctx, s.db, func(tx *gorm.DB) error {
		create := tx
		if mapping.Spec.Key != nil {
			create = create.Clauses(destinationKeyConflictTarget())
		}
		write := create.Create(modelMapping)
		if write.Error != nil {
			return write.Error
		}
		if mapping.Spec.Key != nil && write.RowsAffected == 0 {
			return flterrors.ErrLabelSyncConflict
		}
		return s.incrementRevision(tx, orgID, mapping.Spec.ResourceType)
	})
	if err != nil {
		return nil, store.ErrorFromGormError(err)
	}
	return modelMapping.ToApiResource()
}

func (s *labelSyncMappingStore) Update(ctx context.Context, orgID uuid.UUID, mapping *domain.LabelSyncMapping) (*domain.LabelSyncMapping, *domain.LabelSyncMapping, error) {
	if mapping == nil || mapping.Metadata.Name == nil {
		return nil, nil, flterrors.ErrResourceIsNil
	}
	var expectedResourceVersion *int64
	if mapping.Metadata.ResourceVersion != nil {
		value, err := strconv.ParseInt(lo.FromPtr(mapping.Metadata.ResourceVersion), 10, 64)
		if err != nil {
			return nil, nil, flterrors.ErrIllegalResourceVersionFormat
		}
		expectedResourceVersion = &value
	}

	var previous, result *domain.LabelSyncMapping
	err := store.RunInTransaction(ctx, s.db, func(tx *gorm.DB) error {
		current := model.LabelSyncMapping{}
		if err := tx.Where("org_id = ? AND name = ? AND spec IS NOT NULL", orgID, *mapping.Metadata.Name).Take(&current).Error; err != nil {
			return store.ErrorFromGormError(err)
		}
		if current.DeletionTimestamp != nil {
			return flterrors.ErrResourceVersionConflict
		}
		if expectedResourceVersion != nil && lo.FromPtr(current.ResourceVersion) != lo.FromPtr(expectedResourceVersion) {
			return flterrors.ErrResourceVersionConflict
		}
		if current.Spec.Data.ResourceType != mapping.Spec.ResourceType {
			return flterrors.ErrResourceVersionConflict
		}

		previousResource, err := current.ToApiResource()
		if err != nil {
			return err
		}
		previous = previousResource
		updated := current
		updated.Spec = model.MakeJSONField(mapping.Spec)
		specChanged := !updated.HasSameSpecAs(&current)
		if specChanged {
			updated.Generation = lo.ToPtr(lo.FromPtr(current.Generation) + 1)
			updated.Status = model.MakeJSONField(lo.FromPtr(mapping.Status))
		} else {
			updated.Generation = cloneInt64(current.Generation)
			updated.Status = current.Status
		}

		if mapping.Metadata.Labels != nil {
			updated.Labels = lo.FromPtr(mapping.Metadata.Labels)
		}
		if mapping.Metadata.Annotations != nil {
			updated.Annotations = lo.FromPtr(mapping.Metadata.Annotations)
		}
		updated.ResourceVersion = lo.ToPtr(lo.FromPtr(current.ResourceVersion) + 1)

		updates := map[string]interface{}{
			"spec":             updated.Spec,
			"status":           updated.Status,
			"generation":       updated.Generation,
			"resource_version": updated.ResourceVersion,
		}
		if mapping.Metadata.Labels != nil {
			updates["labels"] = updated.Labels
		}
		if mapping.Metadata.Annotations != nil {
			updates["annotations"] = updated.Annotations
		}
		write := tx.Model(&model.LabelSyncMapping{}).
			Where("org_id = ? AND name = ? AND id = ? AND resource_version = ?", orgID, current.Name, current.ID, lo.FromPtr(current.ResourceVersion)).
			Updates(updates)
		if write.Error != nil {
			if errors.Is(write.Error, gorm.ErrDuplicatedKey) {
				return flterrors.ErrLabelSyncConflict
			}
			return store.ErrorFromGormError(write.Error)
		}
		if write.RowsAffected == 0 {
			return flterrors.ErrResourceVersionConflict
		}
		if specChanged {
			if err := s.incrementRevision(tx, orgID, mapping.Spec.ResourceType); err != nil {
				return err
			}
		}
		result, err = updated.ToApiResource()
		return err
	})
	return result, previous, err
}

func (s *labelSyncMappingStore) Get(ctx context.Context, orgID uuid.UUID, name string) (*domain.LabelSyncMapping, error) {
	return s.genericStore.Get(ctx, orgID, name)
}

func (s *labelSyncMappingStore) List(ctx context.Context, orgID uuid.UUID, params store.ListParams) (*domain.LabelSyncMappingList, error) {
	return s.genericStore.List(ctx, orgID, params)
}

func (s *labelSyncMappingStore) Delete(ctx context.Context, orgID uuid.UUID, name string) (bool, error) {
	deleted := false
	err := store.RunInTransaction(ctx, s.db, func(tx *gorm.DB) error {
		var mappingID uuid.UUID
		mappingIDCaptured := false
		for {
			mapping := model.LabelSyncMapping{}
			query := tx.Where("org_id = ? AND name = ? AND spec IS NOT NULL", orgID, name)
			if mappingIDCaptured {
				query = query.Where("id = ?", mappingID)
			}
			if err := query.Take(&mapping).Error; err != nil {
				return store.ErrorFromGormError(err)
			}
			if !mappingIDCaptured {
				mappingID = mapping.ID
				mappingIDCaptured = true
			}
			if mapping.DeletionTimestamp != nil {
				deleted = true
				return nil
			}

			currentResourceVersion := lo.FromPtr(mapping.ResourceVersion)
			resourceVersion := currentResourceVersion + 1
			deletionTimestamp := time.Now().UTC()
			pending := model.MakeJSONField(pendingStatus(lo.FromPtr(mapping.Generation)))
			write := tx.Model(&model.LabelSyncMapping{}).
				Where("org_id = ? AND name = ? AND id = ? AND resource_version = ? AND deletion_timestamp IS NULL", orgID, name, mappingID, currentResourceVersion).
				Updates(map[string]interface{}{
					"deletion_timestamp": deletionTimestamp,
					"deletion_revision":  resourceVersion,
					"resource_version":   resourceVersion,
					"status":             pending,
				})
			if write.Error != nil {
				return store.ErrorFromGormError(write.Error)
			}
			if write.RowsAffected == 0 {
				continue
			}

			deleted = true
			return s.incrementRevision(tx, orgID, mapping.Spec.Data.ResourceType)
		}
	})
	if errors.Is(err, flterrors.ErrResourceNotFound) {
		return false, nil
	}
	return deleted, err
}

func (s *labelSyncMappingStore) FinalizeDelete(ctx context.Context, orgID uuid.UUID, name string) (bool, error) {
	finalized := false
	err := store.RunInTransaction(ctx, s.db, func(tx *gorm.DB) error {
		mapping := model.LabelSyncMapping{}
		if err := tx.Where("org_id = ? AND name = ? AND deletion_timestamp IS NOT NULL", orgID, name).
			Take(&mapping).Error; err != nil {
			return store.ErrorFromGormError(err)
		}
		resourceType := mapping.Spec.Data.ResourceType
		const savepoint = "label_sync_mapping_finalize_delete"
		if err := tx.SavePoint(savepoint).Error; err != nil {
			return err
		}
		result := tx.Unscoped().Where("org_id = ? AND name = ? AND id = ? AND deletion_timestamp IS NOT NULL", orgID, name, mapping.ID).Delete(&model.LabelSyncMapping{})
		if result.Error != nil {
			if errors.Is(result.Error, gorm.ErrForeignKeyViolated) {
				if err := tx.RollbackTo(savepoint).Error; err != nil {
					return err
				}
				return nil
			}
			return store.ErrorFromGormError(result.Error)
		}
		if result.RowsAffected == 0 {
			return nil
		}
		finalized = true
		return s.incrementRevision(tx, orgID, resourceType)
	})
	if errors.Is(err, flterrors.ErrResourceNotFound) {
		return false, nil
	}
	return finalized, err
}

func (s *labelSyncMappingStore) incrementRevision(tx *gorm.DB, orgID uuid.UUID, resourceType domain.LabelSyncMappingResourceType) error {
	state := model.LabelSyncState{OrgID: orgID, ResourceType: string(resourceType), Revision: 0}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&state).Error; err != nil {
		return err
	}
	result := tx.Model(&model.LabelSyncState{}).
		Where("org_id = ? AND resource_type = ?", orgID, resourceType).
		UpdateColumn("revision", gorm.Expr("revision + 1"))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("label-sync state row was not updated")
	}
	return nil
}

func destinationKeyConflictTarget() clause.OnConflict {
	return clause.OnConflict{
		Columns: []clause.Column{
			{Name: "org_id"},
			{Name: "(spec->>'resourceType')", Raw: true},
			{Name: "(spec->>'key')", Raw: true},
		},
		TargetWhere: clause.Where{Exprs: []clause.Expression{
			clause.Expr{SQL: "spec->>'key' IS NOT NULL"},
		}},
		DoNothing: true,
	}
}

func pendingStatus(generation int64) domain.LabelSyncMappingStatus {
	conditions := []domain.Condition{{
		Type:               domain.ConditionTypeLabelSyncMappingReady,
		Status:             domain.ConditionStatusFalse,
		Reason:             "Pending",
		Message:            "Mapping propagation is pending",
		ObservedGeneration: lo.ToPtr(generation),
	}}
	return domain.LabelSyncMappingStatus{Conditions: &conditions}
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
