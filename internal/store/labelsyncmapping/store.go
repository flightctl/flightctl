package labelsyncmapping

import (
	"context"
	"errors"
	"sort"
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

type Store interface {
	InitialMigration(context.Context) error
	Create(context.Context, uuid.UUID, *domain.LabelSyncMapping) (*domain.LabelSyncMapping, error)
	Update(context.Context, uuid.UUID, *domain.LabelSyncMapping) (*domain.LabelSyncMapping, *domain.LabelSyncMapping, error)
	Get(context.Context, uuid.UUID, string) (*domain.LabelSyncMapping, error)
	List(context.Context, uuid.UUID, store.ListParams) (*domain.LabelSyncMappingList, error)
	Delete(context.Context, uuid.UUID, string) (bool, error)
	FinalizeDelete(context.Context, uuid.UUID, string) (bool, error)
	Revision(context.Context, uuid.UUID, domain.LabelSyncMappingResourceType) (int64, error)
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
		`CREATE UNIQUE INDEX label_sync_mappings_org_resource_destination_key_uq ON label_sync_mappings (org_id, (spec->>'resourceType'), (spec->>'key')) WHERE spec->>'key' IS NOT NULL`,
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
		"label_sync_mappings_org_resource_destination_key_uq",
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
		if err := lockMappingSet(tx, orgID, mapping.Spec.ResourceType); err != nil {
			return err
		}
		if mapping.Spec.Key != nil {
			if err := lockLabelKeys(tx, orgID, mapping.Spec.ResourceType, []string{*mapping.Spec.Key}); err != nil {
				return err
			}
		}
		if err := lockState(tx, orgID, mapping.Spec.ResourceType); err != nil {
			return err
		}
		if mapping.Spec.Key != nil {
			if err := admitScalarKey(tx, orgID, mapping.Spec.ResourceType, *mapping.Spec.Key, modelMapping.ID); err != nil {
				return err
			}
		}
		if err := tx.Create(modelMapping).Error; err != nil {
			return store.ErrorFromGormError(err)
		}
		return s.incrementRevision(tx, orgID, mapping.Spec.ResourceType)
	})
	if err != nil {
		return nil, err
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
		if err := lockMappingSet(tx, orgID, mapping.Spec.ResourceType); err != nil {
			return err
		}
		current := model.LabelSyncMapping{}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND name = ? AND spec IS NOT NULL", orgID, *mapping.Metadata.Name).Take(&current).Error; err != nil {
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
			keys := []string{}
			if current.Spec.Data.Key != nil {
				keys = append(keys, *current.Spec.Data.Key)
			}
			if mapping.Spec.Key != nil {
				keys = append(keys, *mapping.Spec.Key)
			}
			if err := lockLabelKeys(tx, orgID, mapping.Spec.ResourceType, keys); err != nil {
				return err
			}
			if err := lockState(tx, orgID, mapping.Spec.ResourceType); err != nil {
				return err
			}
			if mapping.Spec.Key != nil && !sameKey(current.Spec.Data.Key, mapping.Spec.Key) {
				if err := admitScalarKey(tx, orgID, mapping.Spec.ResourceType, *mapping.Spec.Key, current.ID); err != nil {
					return err
				}
			}
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
			Where("org_id = ? AND name = ? AND resource_version = ?", orgID, current.Name, lo.FromPtr(current.ResourceVersion)).
			Updates(updates)
		if write.Error != nil {
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
		if err := lockMappingSet(tx, orgID, domain.LabelSyncMappingDevice); err != nil {
			return err
		}
		mapping := model.LabelSyncMapping{}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND name = ? AND spec IS NOT NULL", orgID, name).Take(&mapping).Error; err != nil {
			return store.ErrorFromGormError(err)
		}
		if mapping.DeletionTimestamp != nil {
			deleted = true
			return nil
		}
		resourceType := mapping.Spec.Data.ResourceType
		if mapping.Spec.Data.Key != nil {
			if err := lockLabelKeys(tx, orgID, resourceType, []string{*mapping.Spec.Data.Key}); err != nil {
				return err
			}
		}
		if err := lockState(tx, orgID, resourceType); err != nil {
			return err
		}

		deletionTimestamp := time.Now().UTC()
		resourceVersion := lo.FromPtr(mapping.ResourceVersion) + 1
		mapping.DeletionTimestamp = &deletionTimestamp
		mapping.DeletionRevision = &resourceVersion
		mapping.ResourceVersion = &resourceVersion
		pending := pendingStatus(lo.FromPtr(mapping.Generation))
		mapping.Status = model.MakeJSONField(pending)
		write := tx.Model(&model.LabelSyncMapping{}).
			Where("org_id = ? AND name = ? AND resource_version = ?", orgID, name, resourceVersion-1).
			Updates(map[string]interface{}{
				"deletion_timestamp": deletionTimestamp,
				"deletion_revision":  resourceVersion,
				"resource_version":   resourceVersion,
				"status":             mapping.Status,
			})
		if write.Error != nil {
			return store.ErrorFromGormError(write.Error)
		}
		if write.RowsAffected == 0 {
			return flterrors.ErrResourceVersionConflict
		}
		deleted = true
		return s.incrementRevision(tx, orgID, resourceType)
	})
	if errors.Is(err, flterrors.ErrResourceNotFound) {
		return false, nil
	}
	return deleted, err
}

func (s *labelSyncMappingStore) FinalizeDelete(ctx context.Context, orgID uuid.UUID, name string) (bool, error) {
	finalized := false
	err := store.RunInTransaction(ctx, s.db, func(tx *gorm.DB) error {
		if err := lockMappingSet(tx, orgID, domain.LabelSyncMappingDevice); err != nil {
			return err
		}
		mapping := model.LabelSyncMapping{}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("org_id = ? AND name = ? AND deletion_timestamp IS NOT NULL", orgID, name).
			Take(&mapping).Error; err != nil {
			return store.ErrorFromGormError(err)
		}
		resourceType := mapping.Spec.Data.ResourceType
		if mapping.Spec.Data.Key != nil {
			if err := lockLabelKeys(tx, orgID, resourceType, []string{*mapping.Spec.Data.Key}); err != nil {
				return err
			}
		}
		if err := lockState(tx, orgID, resourceType); err != nil {
			return err
		}

		var ownedCount int64
		if err := tx.Model(&model.DeviceLabel{}).Where("org_id = ? AND label_sync_mapping_id = ?", orgID, mapping.ID).Count(&ownedCount).Error; err != nil {
			return err
		}
		if ownedCount > 0 {
			return nil
		}
		result := tx.Unscoped().Where("org_id = ? AND name = ? AND id = ?", orgID, name, mapping.ID).Delete(&model.LabelSyncMapping{})
		if result.Error != nil {
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

func (s *labelSyncMappingStore) Revision(ctx context.Context, orgID uuid.UUID, resourceType domain.LabelSyncMappingResourceType) (int64, error) {
	state := model.LabelSyncState{}
	err := s.getDB(ctx).Where("org_id = ? AND resource_type = ?", orgID, resourceType).Take(&state).Error
	if err == gorm.ErrRecordNotFound {
		return 0, nil
	}
	return state.Revision, err
}

func (s *labelSyncMappingStore) incrementRevision(tx *gorm.DB, orgID uuid.UUID, resourceType domain.LabelSyncMappingResourceType) error {
	result := tx.Model(&model.LabelSyncState{}).
		Where("org_id = ? AND resource_type = ?", orgID, resourceType).
		UpdateColumn("revision", gorm.Expr("revision + 1"))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("label-sync state row was not locked before revision update")
	}
	return nil
}

func lockMappingSet(tx *gorm.DB, orgID uuid.UUID, resourceType domain.LabelSyncMappingResourceType) error {
	return advisoryLock(tx, "mapping-set:"+orgID.String()+":"+string(resourceType))
}

func lockLabelKeys(tx *gorm.DB, orgID uuid.UUID, resourceType domain.LabelSyncMappingResourceType, keys []string) error {
	keys = lo.Uniq(keys)
	sort.Strings(keys)
	for _, key := range keys {
		if err := advisoryLock(tx, "label-key:"+orgID.String()+":"+string(resourceType)+":"+key); err != nil {
			return err
		}
	}
	return nil
}

func advisoryLock(tx *gorm.DB, key string) error {
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	return tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", "flightctl:"+key).Error
}

func lockState(tx *gorm.DB, orgID uuid.UUID, resourceType domain.LabelSyncMappingResourceType) error {
	state := model.LabelSyncState{OrgID: orgID, ResourceType: string(resourceType), Revision: 0}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&state).Error; err != nil {
		return err
	}
	locked := model.LabelSyncState{}
	return tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("org_id = ? AND resource_type = ?", orgID, resourceType).
		Take(&locked).Error
}

func admitScalarKey(tx *gorm.DB, orgID uuid.UUID, resourceType domain.LabelSyncMappingResourceType, key string, mappingID uuid.UUID) error {
	var reservationCount int64
	err := tx.Model(&model.LabelSyncMapping{}).
		Where("org_id = ? AND id <> ? AND spec->>'resourceType' = ? AND spec->>'key' = ?", orgID, mappingID, resourceType, key).
		Count(&reservationCount).Error
	if err != nil {
		return err
	}
	if reservationCount > 0 {
		return flterrors.ErrLabelSyncConflict
	}

	var count int64
	err = tx.Model(&model.DeviceLabel{}).
		Where("org_id = ? AND label_key = ? AND (label_sync_mapping_id IS NULL OR label_sync_mapping_id <> ?)", orgID, key, mappingID).
		Count(&count).Error
	if err != nil {
		return err
	}
	if count > 0 {
		return flterrors.ErrLabelSyncConflict
	}
	return nil
}

func sameKey(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
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

func (s *labelSyncMappingStore) getDB(ctx context.Context) *gorm.DB {
	return s.db.WithContext(ctx)
}
