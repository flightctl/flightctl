package device

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"sort"
	"strconv"
	"strings"

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

// GetLabelSyncMappingIDsByKeys returns distinct current mapping IDs for exact label keys in orgID.
func (s *DeviceStore) GetLabelSyncMappingIDsByKeys(ctx context.Context, orgID uuid.UUID, labelKeys []string) (map[string][]uuid.UUID, error) {
	ownersByKey := make(map[string][]uuid.UUID)
	keys := lo.Uniq(labelKeys)
	if len(keys) == 0 {
		return ownersByKey, nil
	}

	var labels []model.DeviceLabel
	if err := s.getDB(ctx).
		Model(&model.DeviceLabel{}).
		Select("label_key, label_sync_mapping_id").
		Distinct().
		Where("org_id = ? AND label_key IN ? AND label_sync_mapping_id IS NOT NULL", orgID, keys).
		Order("label_key ASC, label_sync_mapping_id ASC").
		Find(&labels).Error; err != nil {
		return nil, store.ErrorFromGormError(err)
	}

	for _, label := range labels {
		if label.LabelSyncMappingID == nil {
			continue
		}
		ownersByKey[label.LabelKey] = append(ownersByKey[label.LabelKey], *label.LabelSyncMappingID)
	}
	return ownersByKey, nil
}

type labelApplyPlan struct {
	resourceVersion  int64
	labels           map[string]string
	annotations      map[string]string
	visibleChanged   bool
	conditionChanged bool
	result           domain.DeviceLabelApplyResult
	updated          domain.Device
}

// ApplyLabels atomically applies labels, their mapping owners, the managed-label
// annotation, and an optional reconciliation condition. Every change advances
// the device resource version before changing ownership rows, including
// owner-only transfers.
func (s *DeviceStore) ApplyLabels(ctx context.Context, orgID uuid.UUID, name string, snapshot domain.DeviceLabelSnapshot, desired map[string]domain.DesiredDeviceLabel, condition *domain.Condition) (domain.DeviceLabelApplyResult, error) {
	plan, err := newLabelApplyPlan(name, snapshot, desired)
	if err != nil {
		return domain.DeviceLabelApplyResult{}, err
	}
	if condition != nil {
		plan.conditionChanged = updateLabelCondition(&plan.updated, *condition)
	}
	changed := plan.visibleChanged || plan.result.OwnershipChanged || plan.conditionChanged
	if !changed && condition == nil {
		plan.result.Device = &plan.updated
		return plan.result, nil
	}

	err = store.RunInTransaction(ctx, s.dbHandler, func(tx *gorm.DB) error {
		if !changed {
			// Confirm no-op observations without rewriting the device.
			var current model.Device
			err := tx.Select("resource_version").Where("org_id = ? AND name = ? AND resource_version = ?", orgID, name, plan.resourceVersion).Take(&current).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return flterrors.ErrResourceVersionConflict
			}
			return store.ErrorFromGormError(err)
		}
		return s.applyLabelsInTransaction(tx, orgID, name, snapshot, desired, plan)
	})
	if err != nil {
		return domain.DeviceLabelApplyResult{}, err
	}
	if changed {
		plan.updated.Metadata.ResourceVersion = lo.ToPtr(strconv.FormatInt(plan.resourceVersion+1, 10))
	}
	plan.result.Device = &plan.updated
	return plan.result, nil
}

func updateLabelCondition(device *domain.Device, condition domain.Condition) bool {
	status := domain.NewDeviceStatus()
	if device.Status != nil {
		status = *device.Status
		status.Conditions = append([]domain.Condition(nil), status.Conditions...)
	}
	previous := domain.FindStatusCondition(status.Conditions, domain.ConditionTypeDeviceLabelsSynced)
	if condition.Status == domain.ConditionStatusTrue && (previous == nil || previous.Status == domain.ConditionStatusTrue) {
		return false
	}
	if previous != nil && previous.Status == condition.Status && previous.Reason == condition.Reason && previous.Message == condition.Message {
		return false
	}
	condition.ObservedGeneration = device.Metadata.Generation
	domain.SetStatusCondition(&status.Conditions, condition)
	device.Status = &status
	return true
}

func newLabelApplyPlan(name string, snapshot domain.DeviceLabelSnapshot, desired map[string]domain.DesiredDeviceLabel) (labelApplyPlan, error) {
	if snapshot.Device.Metadata.Name == nil || *snapshot.Device.Metadata.Name != name {
		return labelApplyPlan{}, flterrors.ErrResourceVersionConflict
	}
	resourceVersion, err := strconv.ParseInt(lo.FromPtr(snapshot.Device.Metadata.ResourceVersion), 10, 64)
	if err != nil {
		return labelApplyPlan{}, flterrors.ErrIllegalResourceVersionFormat
	}

	labels := deviceLabelValues(desired)
	annotations := maps.Clone(lo.FromPtr(snapshot.Device.Metadata.Annotations))
	if annotations == nil {
		annotations = make(map[string]string)
	}
	managedLabelsChanged, err := updateManagedLabelsAnnotation(annotations, desired)
	if err != nil {
		return labelApplyPlan{}, err
	}
	labelsChanged := !maps.Equal(lo.FromPtr(snapshot.Device.Metadata.Labels), labels)
	visibleChanged := labelsChanged || managedLabelsChanged
	currentRows := deviceLabelOwnersByKey(snapshot.Labels)
	currentLabels := lo.FromPtr(snapshot.Device.Metadata.Labels)
	ownershipChanged := deviceLabelOwnershipChanged(currentRows, currentLabels, desired)

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

func deviceLabelValues(desired map[string]domain.DesiredDeviceLabel) map[string]string {
	labels := make(map[string]string, len(desired))
	for key, label := range desired {
		labels[key] = label.Value
	}
	return labels
}

func updateManagedLabelsAnnotation(annotations map[string]string, desired map[string]domain.DesiredDeviceLabel) (bool, error) {
	managedValue, err := managedLabelsValue(desired)
	if err != nil {
		return false, err
	}
	oldValue, hadValue := annotations[domain.DeviceAnnotationManagedLabels]
	if managedValue == nil {
		delete(annotations, domain.DeviceAnnotationManagedLabels)
	} else {
		annotations[domain.DeviceAnnotationManagedLabels] = *managedValue
	}
	newValue, hasValue := annotations[domain.DeviceAnnotationManagedLabels]
	return hadValue != hasValue || oldValue != newValue, nil
}

func deviceLabelOwnershipChanged(currentRows map[string]domain.DeviceLabelOwnership, currentLabels map[string]string, desired map[string]domain.DesiredDeviceLabel) bool {
	for key, label := range desired {
		previous, exists := currentRows[key]
		if !exists && label.MappingID != nil {
			return true
		}
		if exists && !sameDeviceLabelOwner(previous.MappingID, label.MappingID) {
			return true
		}
	}
	for key, previous := range currentRows {
		_, existsInDevice := currentLabels[key]
		_, remainsDesired := desired[key]
		if existsInDevice && !remainsDesired && previous.MappingID != nil {
			return true
		}
	}
	return false
}

func (s *DeviceStore) applyLabelsInTransaction(tx *gorm.DB, orgID uuid.UUID, name string, snapshot domain.DeviceLabelSnapshot, desired map[string]domain.DesiredDeviceLabel, plan labelApplyPlan) error {
	currentRowsByKey := deviceLabelOwnersByKey(snapshot.Labels)
	if plan.visibleChanged || plan.result.OwnershipChanged || plan.conditionChanged {
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
	if plan.conditionChanged {
		device, err := model.NewDeviceFromApiResource(&plan.updated)
		if err != nil {
			return err
		}
		updates["service_conditions"] = device.ServiceConditions
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

func (s *DeviceStore) rejectManagedLabelChanges(ctx context.Context, orgID uuid.UUID, deviceName string, before, after map[string]string) error {
	changedKeys := changedDeviceLabelKeys(before, after)
	if len(changedKeys) == 0 {
		return nil
	}
	var ownedCount int64
	if err := s.getDB(ctx).Model(&model.DeviceLabel{}).
		Where("org_id = ? AND device_name = ? AND label_key IN ? AND label_sync_mapping_id IS NOT NULL", orgID, deviceName, changedKeys).
		Count(&ownedCount).Error; err != nil {
		return store.ErrorFromGormError(err)
	}
	if ownedCount > 0 {
		return flterrors.ErrManagedLabelConflict
	}
	return nil
}

func (s *DeviceStore) preserveOmittedManagedLabels(
	ctx context.Context,
	orgID uuid.UUID,
	existing, updated *model.Device,
	device *domain.Device,
	explicitRemovalPaths map[string]struct{},
) error {
	var managedLabels []model.DeviceLabel
	if err := s.getDB(ctx).
		Select("label_key").
		Where("org_id = ? AND device_name = ? AND label_sync_mapping_id IS NOT NULL", orgID, existing.Name).
		Find(&managedLabels).Error; err != nil {
		return store.ErrorFromGormError(err)
	}
	if len(managedLabels) == 0 {
		return nil
	}

	labels := make(model.JSONMap[string, string], len(updated.Labels)+len(managedLabels))
	for key, value := range updated.Labels {
		labels[key] = value
	}
	changed := false
	for _, managed := range managedLabels {
		path := "/metadata/labels/" + strings.ReplaceAll(strings.ReplaceAll(managed.LabelKey, "~", "~0"), "/", "~1")
		if _, explicitlyRemoved := explicitRemovalPaths[path]; explicitlyRemoved {
			// Keep an explicit removal absent so the managed-label guard rejects it
			// instead of silently restoring the value.
			continue
		}
		if _, exists := labels[managed.LabelKey]; exists {
			continue
		}
		value, exists := existing.Labels[managed.LabelKey]
		if !exists {
			continue
		}
		labels[managed.LabelKey] = value
		changed = true
	}
	if !changed {
		return nil
	}

	updated.Labels = labels
	apiLabels := make(map[string]string, len(labels))
	for key, value := range labels {
		apiLabels[key] = value
	}
	device.Metadata.Labels = &apiLabels
	return nil
}

func changedDeviceLabelKeys(before, after map[string]string) []string {
	keys := make(map[string]struct{}, len(before)+len(after))
	for key := range before {
		keys[key] = struct{}{}
	}
	for key := range after {
		keys[key] = struct{}{}
	}
	changed := make([]string, 0, len(keys))
	for key := range keys {
		beforeValue, beforeExists := before[key]
		afterValue, afterExists := after[key]
		if beforeExists != afterExists || beforeValue != afterValue {
			changed = append(changed, key)
		}
	}
	sort.Strings(changed)
	return changed
}
