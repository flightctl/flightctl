package labelsyncmapping

import (
	"errors"
	"fmt"
	"sort"

	"github.com/flightctl/flightctl/internal/domain"
	labelsyncmappingstore "github.com/flightctl/flightctl/internal/store/labelsyncmapping"
	"github.com/google/uuid"
	"github.com/samber/lo"
)

const maxManagedDeviceLabels = 100

type mappingCandidate struct {
	mappingID uuid.UUID
	value     string
	mapMode   bool
}

type desiredLabelState struct {
	currentLabels      map[string]string
	currentOwners      map[string]*uuid.UUID
	currentOwnedByMap  map[uuid.UUID][]domain.DeviceLabelOwnership
	desired            map[string]domain.DesiredDeviceLabel
	activeMappings     []labelsyncmappingstore.DeviceMapping
	mappingNames       map[uuid.UUID]string
	failures           map[uuid.UUID][]error
	outputsByMapping   map[uuid.UUID]map[string]string
	candidatesByKey    map[string][]mappingCandidate
	scalarReservations map[string]map[uuid.UUID]struct{}
}

type deviceReconciliationSnapshot struct {
	Device          domain.DeviceLabelSnapshot
	Mappings        []labelsyncmappingstore.DeviceMapping
	MappingRevision int64
}

func desiredDeviceLabels(snapshot deviceReconciliationSnapshot, evaluator Evaluator) (map[string]domain.DesiredDeviceLabel, []MappingOutcome, error) {
	activation, err := ActivateDevice(snapshot.Device.Device)
	if err != nil {
		return nil, nil, err
	}

	state := newDesiredLabelState(snapshot)
	state.evaluateMappings(evaluator, activation)
	state.resolveCandidates()
	state.enforceManagedLabelLimit()
	return state.desired, state.outcomes(), nil
}

func newDesiredLabelState(snapshot deviceReconciliationSnapshot) *desiredLabelState {
	currentLabels := lo.FromPtr(snapshot.Device.Device.Metadata.Labels)
	state := &desiredLabelState{
		currentLabels:      currentLabels,
		currentOwners:      make(map[string]*uuid.UUID, len(snapshot.Device.Labels)),
		currentOwnedByMap:  make(map[uuid.UUID][]domain.DeviceLabelOwnership),
		desired:            make(map[string]domain.DesiredDeviceLabel, len(currentLabels)),
		activeMappings:     make([]labelsyncmappingstore.DeviceMapping, 0, len(snapshot.Mappings)),
		mappingNames:       make(map[uuid.UUID]string, len(snapshot.Mappings)),
		failures:           make(map[uuid.UUID][]error),
		outputsByMapping:   make(map[uuid.UUID]map[string]string),
		candidatesByKey:    make(map[string][]mappingCandidate),
		scalarReservations: make(map[string]map[uuid.UUID]struct{}),
	}

	for _, label := range snapshot.Device.Labels {
		state.currentOwners[label.Key] = label.MappingID
		if label.MappingID != nil {
			state.currentOwnedByMap[*label.MappingID] = append(state.currentOwnedByMap[*label.MappingID], label)
		}
	}
	for key, value := range currentLabels {
		owner, found := state.currentOwners[key]
		if !found || owner == nil {
			state.desired[key] = domain.DesiredDeviceLabel{Value: value}
		}
	}
	for _, entry := range snapshot.Mappings {
		mapping := entry.Mapping
		if mapping.Spec.ResourceType != domain.LabelSyncMappingDevice {
			continue
		}
		if mapping.Spec.Key != nil {
			state.addScalarReservation(*mapping.Spec.Key, entry.ID)
		}
		if mapping.Metadata.DeletionTimestamp != nil {
			continue
		}
		state.activeMappings = append(state.activeMappings, entry)
		state.mappingNames[entry.ID] = lo.FromPtr(mapping.Metadata.Name)
	}
	return state
}

func (s *desiredLabelState) addScalarReservation(key string, mappingID uuid.UUID) {
	if s.scalarReservations[key] == nil {
		s.scalarReservations[key] = make(map[uuid.UUID]struct{})
	}
	s.scalarReservations[key][mappingID] = struct{}{}
}

func (s *desiredLabelState) evaluateMappings(evaluator Evaluator, activation Activation) {
	for _, entry := range s.activeMappings {
		outputs, err := mappingOutputs(evaluator, entry.Mapping, activation)
		if err != nil {
			s.failures[entry.ID] = append(s.failures[entry.ID], fmt.Errorf("evaluating mapping %q: %w", s.mappingNames[entry.ID], err))
			retainMappingOutputs(s.desired, s.currentOwnedByMap, entry.ID)
			continue
		}
		s.outputsByMapping[entry.ID] = outputs
		mapMode := entry.Mapping.Spec.Key == nil
		for key, value := range outputs {
			s.candidatesByKey[key] = append(s.candidatesByKey[key], mappingCandidate{mappingID: entry.ID, value: value, mapMode: mapMode})
		}
	}
}

func (s *desiredLabelState) resolveCandidates() {
	keys := make([]string, 0, len(s.candidatesByKey))
	for key := range s.candidatesByKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		candidates := s.candidatesByKey[key]
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].mappingID.String() < candidates[j].mappingID.String() })
		s.resolveKey(key, candidates)
	}
}

func (s *desiredLabelState) resolveKey(key string, candidates []mappingCandidate) {
	if len(candidates) > 1 {
		err := fmt.Errorf("label key %q is emitted by multiple mappings", key)
		for _, candidate := range candidates {
			s.failures[candidate.mappingID] = append(s.failures[candidate.mappingID], fmt.Errorf("mapping %q: %w", s.mappingNames[candidate.mappingID], err))
		}
		retainCollidingOwner(s.desired, s.currentLabels, s.currentOwners, key, candidates)
		return
	}
	s.resolveCandidate(key, candidates[0])
}

func (s *desiredLabelState) resolveCandidate(key string, candidate mappingCandidate) {
	if candidate.mapMode && s.mapCandidateBlocked(key, candidate) {
		return
	}
	if existing, exists := s.desired[key]; exists && existing.MappingID != nil && *existing.MappingID != candidate.mappingID {
		recordCandidateFailure(s.failures, s.mappingNames, candidate, fmt.Errorf("label key %q is retained by another mapping", key))
		return
	}
	s.desired[key] = domain.DesiredDeviceLabel{Value: candidate.value, MappingID: lo.ToPtr(candidate.mappingID)}
}

func (s *desiredLabelState) mapCandidateBlocked(key string, candidate mappingCandidate) bool {
	if hasOtherScalarReservation(s.scalarReservations[key], candidate.mappingID) {
		recordCandidateFailure(s.failures, s.mappingNames, candidate, fmt.Errorf("map output %q conflicts with a scalar reservation", key))
		retainMappingKey(s.desired, s.currentLabels, s.currentOwners, key, candidate.mappingID)
		return true
	}
	return false
}

func (s *desiredLabelState) enforceManagedLabelLimit() {
	managedCount := 0
	for _, label := range s.desired {
		if label.MappingID != nil {
			managedCount++
		}
	}
	if managedCount <= maxManagedDeviceLabels {
		return
	}
	for _, entry := range s.activeMappings {
		outputs, evaluated := s.outputsByMapping[entry.ID]
		if !evaluated || len(outputs) == 0 {
			continue
		}
		s.failures[entry.ID] = append(s.failures[entry.ID], fmt.Errorf("mapping %q exceeds the device managed-label limit of %d", s.mappingNames[entry.ID], maxManagedDeviceLabels))
		removeMappingOutputs(s.desired, entry.ID)
		retainMappingOutputs(s.desired, s.currentOwnedByMap, entry.ID)
	}
}

func (s *desiredLabelState) outcomes() []MappingOutcome {
	outcomes := make([]MappingOutcome, len(s.activeMappings))
	for i, entry := range s.activeMappings {
		outcomes[i] = MappingOutcome{MappingID: entry.ID, Err: errors.Join(s.failures[entry.ID]...)}
	}
	return outcomes
}

func mappingOutputs(evaluator Evaluator, mapping domain.LabelSyncMapping, activation Activation) (map[string]string, error) {
	result, err := evaluator.Evaluate(mapping.Spec.Expression, activation)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("evaluator returned no result without an error")
	}
	if _, ok := result.(NoResult); ok {
		return map[string]string{}, nil
	}

	if mapping.Spec.Key != nil {
		scalar, ok := result.(ScalarResult)
		if !ok {
			return nil, fmt.Errorf("scalar mapping returned result type %T", result)
		}
		return map[string]string{*mapping.Spec.Key: string(scalar)}, nil
	}

	mapResult, ok := result.(MapResult)
	if !ok {
		return nil, fmt.Errorf("map mapping returned result type %T", result)
	}
	return map[string]string(mapResult), nil
}

func retainMappingOutputs(desired map[string]domain.DesiredDeviceLabel, owned map[uuid.UUID][]domain.DeviceLabelOwnership, mappingID uuid.UUID) {
	for _, label := range owned[mappingID] {
		desired[label.Key] = domain.DesiredDeviceLabel{Value: label.Value, MappingID: lo.ToPtr(mappingID)}
	}
}

func retainMappingKey(desired map[string]domain.DesiredDeviceLabel, currentLabels map[string]string, currentOwners map[string]*uuid.UUID, key string, mappingID uuid.UUID) {
	owner := currentOwners[key]
	if owner == nil || *owner != mappingID {
		return
	}
	if value, exists := currentLabels[key]; exists {
		desired[key] = domain.DesiredDeviceLabel{Value: value, MappingID: lo.ToPtr(mappingID)}
	}
}

func retainCollidingOwner(desired map[string]domain.DesiredDeviceLabel, currentLabels map[string]string, currentOwners map[string]*uuid.UUID, key string, candidates []mappingCandidate) {
	owner := currentOwners[key]
	if owner == nil {
		return
	}
	for _, candidate := range candidates {
		if candidate.mappingID == *owner {
			retainMappingKey(desired, currentLabels, currentOwners, key, *owner)
			return
		}
	}
}

func hasOtherScalarReservation(reservations map[uuid.UUID]struct{}, mappingID uuid.UUID) bool {
	for id := range reservations {
		if id != mappingID {
			return true
		}
	}
	return false
}

func recordCandidateFailure(failures map[uuid.UUID][]error, mappingNames map[uuid.UUID]string, candidate mappingCandidate, err error) {
	failures[candidate.mappingID] = append(failures[candidate.mappingID], fmt.Errorf("mapping %q: %w", mappingNames[candidate.mappingID], err))
}

func removeMappingOutputs(desired map[string]domain.DesiredDeviceLabel, mappingID uuid.UUID) {
	for key, label := range desired {
		if label.MappingID != nil && *label.MappingID == mappingID {
			delete(desired, key)
		}
	}
}
