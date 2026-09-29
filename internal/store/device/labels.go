package device

import (
	"context"
	"database/sql"
	"encoding/json"
	"maps"
	"sort"
	"strconv"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"gorm.io/gorm"
)

// GetLabelSnapshot reads the device and its label ownership rows from one
// repeatable-read snapshot.
func (s *DeviceStore) GetLabelSnapshot(ctx context.Context, orgID uuid.UUID, name string) (domain.DeviceLabelSnapshot, error) {
	var snapshot domain.DeviceLabelSnapshot
	err := s.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		var device model.Device
		if err := tx.Where("org_id = ? AND name = ?", orgID, name).Take(&device).Error; err != nil {
			return store.ErrorFromGormError(err)
		}
		resource, err := device.ToApiResource()
		if err != nil {
			return err
		}
		snapshot.Device = *resource

		var labels []model.DeviceLabel
		if err := tx.Where("org_id = ? AND device_name = ?", orgID, name).Order("label_key ASC").Find(&labels).Error; err != nil {
			return store.ErrorFromGormError(err)
		}
		snapshot.Labels = make([]domain.DeviceLabelOwnership, len(labels))
		for i, label := range labels {
			snapshot.Labels[i] = domain.DeviceLabelOwnership{
				Key:       label.LabelKey,
				Value:     label.LabelValue,
				MappingID: label.LabelSyncMappingID,
			}
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return domain.DeviceLabelSnapshot{}, err
	}
	return snapshot, nil
}

type labelApplyPlan struct {
	resourceVersion int64
	labels          map[string]string
	annotations     map[string]string
	visibleChanged  bool
	result          domain.DeviceLabelApplyResult
	updated         domain.Device
}

// ApplyLabels atomically applies labels, their mapping owners, and the
// managed-label annotation. Every state change advances the device resource
// version before changing ownership rows, including owner-only transfers.
func (s *DeviceStore) ApplyLabels(ctx context.Context, orgID uuid.UUID, name string, snapshot domain.DeviceLabelSnapshot, desired map[string]domain.DesiredDeviceLabel) (domain.DeviceLabelApplyResult, error) {
	plan, err := newLabelApplyPlan(name, snapshot, desired)
	if err != nil {
		return domain.DeviceLabelApplyResult{}, err
	}
	if !plan.visibleChanged && !plan.result.OwnershipChanged {
		plan.result.Device = &plan.updated
		return plan.result, nil
	}

	err = store.RunInTransaction(ctx, s.dbHandler, func(tx *gorm.DB) error {
		return s.applyLabelsInTransaction(tx, orgID, name, snapshot, desired, plan)
	})
	if err != nil {
		return domain.DeviceLabelApplyResult{}, err
	}
	if plan.visibleChanged || plan.result.OwnershipChanged {
		plan.updated.Metadata.ResourceVersion = lo.ToPtr(strconv.FormatInt(plan.resourceVersion+1, 10))
	}
	plan.result.Device = &plan.updated
	return plan.result, nil
}

func newLabelApplyPlan(name string, snapshot domain.DeviceLabelSnapshot, desired map[string]domain.DesiredDeviceLabel) (labelApplyPlan, error) {
	if snapshot.Device.Metadata.Name == nil || *snapshot.Device.Metadata.Name != name {
		return labelApplyPlan{}, flterrors.ErrResourceVersionConflict
	}
	resourceVersion, err := strconv.ParseInt(lo.FromPtr(snapshot.Device.Metadata.ResourceVersion), 10, 64)
	if err != nil {
		return labelApplyPlan{}, flterrors.ErrIllegalResourceVersionFormat
	}

	labels := make(map[string]string, len(desired))
	for key, label := range desired {
		labels[key] = label.Value
	}
	managedValue, err := managedLabelsValue(desired)
	if err != nil {
		return labelApplyPlan{}, err
	}
	annotations := maps.Clone(lo.FromPtr(snapshot.Device.Metadata.Annotations))
	if annotations == nil {
		annotations = make(map[string]string)
	}
	oldManagedValue, hadManagedValue := annotations[domain.DeviceAnnotationManagedLabels]
	if managedValue == nil {
		delete(annotations, domain.DeviceAnnotationManagedLabels)
	} else {
		annotations[domain.DeviceAnnotationManagedLabels] = *managedValue
	}
	newManagedValue, hasManagedValue := annotations[domain.DeviceAnnotationManagedLabels]
	managedLabelsChanged := hadManagedValue != hasManagedValue || oldManagedValue != newManagedValue
	labelsChanged := !maps.Equal(lo.FromPtr(snapshot.Device.Metadata.Labels), labels)
	visibleChanged := labelsChanged || managedLabelsChanged
	currentRows := deviceLabelOwnersByKey(snapshot.Labels)
	currentLabels := lo.FromPtr(snapshot.Device.Metadata.Labels)
	ownershipChanged := false
	for key, label := range desired {
		previous, exists := currentRows[key]
		if !exists {
			ownershipChanged = ownershipChanged || label.MappingID != nil
		} else if !sameDeviceLabelOwner(previous.MappingID, label.MappingID) {
			ownershipChanged = true
		}
	}
	for key, previous := range currentRows {
		_, existsInDevice := currentLabels[key]
		_, remainsDesired := desired[key]
		if existsInDevice && !remainsDesired && previous.MappingID != nil {
			ownershipChanged = true
		}
	}

	updated := snapshot.Device
	updated.Metadata.Labels = &labels
	if len(annotations) == 0 {
		updated.Metadata.Annotations = nil
	} else {
		updated.Metadata.Annotations = &annotations
	}
	return labelApplyPlan{
		resourceVersion: resourceVersion,
		labels:          labels,
		annotations:     annotations,
		visibleChanged:  visibleChanged,
		updated:         updated,
		result: domain.DeviceLabelApplyResult{
			LabelsChanged:        labelsChanged,
			ManagedLabelsChanged: managedLabelsChanged,
			OwnershipChanged:     ownershipChanged,
		},
	}, nil
}

func (s *DeviceStore) applyLabelsInTransaction(tx *gorm.DB, orgID uuid.UUID, name string, snapshot domain.DeviceLabelSnapshot, desired map[string]domain.DesiredDeviceLabel, plan labelApplyPlan) error {
	currentRowsByKey := deviceLabelOwnersByKey(snapshot.Labels)
	if plan.visibleChanged || plan.result.OwnershipChanged {
		if err := updateDeviceLabels(tx, orgID, name, plan.resourceVersion, plan); err != nil {
			return err
		}
	}
	return updateDeviceLabelOwners(tx, orgID, name, currentRowsByKey, desired)
}

func updateDeviceLabels(tx *gorm.DB, orgID uuid.UUID, name string, resourceVersion int64, plan labelApplyPlan) error {
	updates := map[string]interface{}{
		"resource_version": gorm.Expr("resource_version + 1"),
	}
	if plan.visibleChanged {
		var alias *string
		if value, ok := plan.labels["alias"]; ok {
			alias = &value
		}
		updates["labels"] = model.MakeJSONMap(plan.labels)
		updates["alias"] = alias
		updates["annotations"] = model.MakeJSONMap(plan.annotations)
	}
	write := tx.Model(&model.Device{Resource: model.Resource{OrgID: orgID, Name: name}}).
		Where("resource_version = ?", resourceVersion).
		Updates(updates)
	if write.Error != nil {
		return store.ErrorFromGormError(write.Error)
	}
	if write.RowsAffected != 1 {
		return flterrors.ErrResourceVersionConflict
	}
	return nil
}

func updateDeviceLabelOwners(tx *gorm.DB, orgID uuid.UUID, name string, currentRowsByKey map[string]domain.DeviceLabelOwnership, desired map[string]domain.DesiredDeviceLabel) error {
	for key, label := range desired {
		previous, exists := currentRowsByKey[key]
		if exists && sameDeviceLabelOwner(previous.MappingID, label.MappingID) {
			continue
		}
		if !exists && label.MappingID == nil {
			continue
		}

		write := tx.Model(&model.DeviceLabel{}).
			Where("org_id = ? AND device_name = ? AND label_key = ? AND label_value = ?", orgID, name, key, label.Value)
		if previous.MappingID == nil {
			write = write.Where("label_sync_mapping_id IS NULL")
		} else {
			write = write.Where("label_sync_mapping_id = ?", *previous.MappingID)
		}
		write = write.Update("label_sync_mapping_id", label.MappingID)
		if write.Error != nil {
			return store.ErrorFromGormError(write.Error)
		}
		if write.RowsAffected != 1 {
			return flterrors.ErrResourceVersionConflict
		}
	}
	return nil
}

func managedLabelsValue(desired map[string]domain.DesiredDeviceLabel) (*string, error) {
	keys := make([]string, 0, len(desired))
	for key, label := range desired {
		if label.MappingID != nil {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil, nil
	}
	sort.Strings(keys)
	value, err := json.Marshal(keys)
	if err != nil {
		return nil, err
	}
	return lo.ToPtr(string(value)), nil
}

func deviceLabelOwnersByKey(labels []domain.DeviceLabelOwnership) map[string]domain.DeviceLabelOwnership {
	owners := make(map[string]domain.DeviceLabelOwnership, len(labels))
	for _, label := range labels {
		owners[label.Key] = label
	}
	return owners
}

func sameDeviceLabelOwner(left, right *uuid.UUID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
