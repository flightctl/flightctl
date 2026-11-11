package labelsyncmapping

import (
	"context"
	"sort"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	labelsyncmappingstore "github.com/flightctl/flightctl/internal/store/labelsyncmapping"
	"github.com/google/uuid"
)

func (h *ServiceHandler) GetDeviceLabelSyncProvenance(ctx context.Context, orgID uuid.UUID, deviceName string) (*domain.LabelSyncProvenanceList, domain.Status) {
	deviceSnapshot, err := h.deviceStore.GetLabelSnapshot(ctx, orgID, deviceName)
	if err != nil {
		return nil, common.StoreErrorToApiStatus(err, false, domain.DeviceKind, &deviceName)
	}
	mappingSnapshot, err := h.store.GetDeviceMappingsSnapshot(ctx, orgID)
	if err != nil {
		return nil, common.StoreErrorToApiStatus(err, false, domain.LabelSyncMappingKind, nil)
	}

	mappingNames := currentMappingNamesByID(mappingSnapshot)
	ownersByKey := make(map[string]map[string]struct{}, len(deviceSnapshot.Labels))
	for _, label := range deviceSnapshot.Labels {
		if label.MappingID == nil {
			continue
		}
		mappingName, found := mappingNames[*label.MappingID]
		if !found {
			continue
		}
		if ownersByKey[label.Key] == nil {
			ownersByKey[label.Key] = make(map[string]struct{})
		}
		ownersByKey[label.Key][mappingName] = struct{}{}
	}

	items := make([]domain.LabelSyncProvenanceItem, 0, len(ownersByKey))
	keys := make([]string, 0, len(ownersByKey))
	for key := range ownersByKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		items = append(items, domain.LabelSyncProvenanceItem{Key: key, Owners: sortedOwnerNames(ownersByKey[key])})
	}
	return &domain.LabelSyncProvenanceList{Items: items}, domain.StatusOK()
}

func (h *ServiceHandler) GetLabelSyncProvenance(ctx context.Context, orgID uuid.UUID, labelKeys []string) (*domain.LabelSyncProvenanceList, domain.Status) {
	if len(labelKeys) == 0 || len(labelKeys) > MaxProvenanceLabelKeys {
		return nil, domain.StatusBadRequest("labelKeys must contain between 1 and 50 keys")
	}
	for _, key := range labelKeys {
		if key == "" {
			return nil, domain.StatusBadRequest("labelKeys must not contain empty keys")
		}
	}

	ownerIDsByKey, err := h.deviceStore.GetLabelSyncMappingIDsByKeys(ctx, orgID, labelKeys)
	if err != nil {
		return nil, common.StoreErrorToApiStatus(err, false, domain.LabelSyncMappingKind, nil)
	}
	mappingSnapshot, err := h.store.GetDeviceMappingsSnapshot(ctx, orgID)
	if err != nil {
		return nil, common.StoreErrorToApiStatus(err, false, domain.LabelSyncMappingKind, nil)
	}
	mappingNames := currentMappingNamesByID(mappingSnapshot)
	declaredOwnersByKey := declaredScalarMappingOwners(mappingSnapshot)

	items := make([]domain.LabelSyncProvenanceItem, 0, len(labelKeys))
	for _, key := range labelKeys {
		owners := make(map[string]struct{}, len(ownerIDsByKey[key]))
		for _, ownerID := range ownerIDsByKey[key] {
			if mappingName, found := mappingNames[ownerID]; found {
				owners[mappingName] = struct{}{}
			}
		}
		for _, mappingName := range declaredOwnersByKey[key] {
			owners[mappingName] = struct{}{}
		}
		if len(owners) == 0 {
			continue
		}
		items = append(items, domain.LabelSyncProvenanceItem{Key: key, Owners: sortedOwnerNames(owners)})
	}
	return &domain.LabelSyncProvenanceList{Items: items}, domain.StatusOK()
}

func declaredScalarMappingOwners(snapshot labelsyncmappingstore.DeviceMappingsSnapshot) map[string][]string {
	ownersByKey := make(map[string][]string)
	for _, entry := range snapshot.Mappings {
		mapping := entry.Mapping
		if mapping.Metadata.DeletionTimestamp != nil || mapping.Spec.ResourceType != domain.LabelSyncMappingDevice || mapping.Spec.Key == nil {
			continue
		}
		if mapping.Metadata.Name == nil || *mapping.Metadata.Name == "" {
			continue
		}
		ownersByKey[*mapping.Spec.Key] = append(ownersByKey[*mapping.Spec.Key], *mapping.Metadata.Name)
	}
	return ownersByKey
}

func currentMappingNamesByID(snapshot labelsyncmappingstore.DeviceMappingsSnapshot) map[uuid.UUID]string {
	mappingNames := make(map[uuid.UUID]string, len(snapshot.Mappings))
	for _, mapping := range snapshot.Mappings {
		if mapping.Mapping.Metadata.DeletionTimestamp != nil || mapping.Mapping.Metadata.Name == nil {
			continue
		}
		mappingNames[mapping.ID] = *mapping.Mapping.Metadata.Name
	}
	return mappingNames
}

func sortedOwnerNames(owners map[string]struct{}) []string {
	names := make([]string, 0, len(owners))
	for name := range owners {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
