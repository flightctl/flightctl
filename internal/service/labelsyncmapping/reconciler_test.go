package labelsyncmapping

import (
	"context"
	"errors"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	eventservice "github.com/flightctl/flightctl/internal/service/events"
	labelsyncmappingstore "github.com/flightctl/flightctl/internal/store/labelsyncmapping"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type evaluatorResponse struct {
	result Result
	err    error
}

type reconciliationEvaluator struct {
	responses map[string]evaluatorResponse
	called    []string
}

func (e *reconciliationEvaluator) ValidateExpressionIs(string, ResultKind) error { return nil }

func (e *reconciliationEvaluator) Evaluate(expression string, _ Activation) (Result, error) {
	e.called = append(e.called, expression)
	response, ok := e.responses[expression]
	if !ok {
		return nil, errors.New("unexpected expression")
	}
	return response.result, response.err
}

type reconciliationStoreStub struct {
	labelsyncmappingstore.Store
	snapshots []labelsyncmappingstore.DeviceMappingsSnapshot
	reads     int
}

func (s *reconciliationStoreStub) GetDeviceMappingsSnapshot(context.Context, uuid.UUID) (labelsyncmappingstore.DeviceMappingsSnapshot, error) {
	if len(s.snapshots) == 0 {
		return labelsyncmappingstore.DeviceMappingsSnapshot{}, nil
	}
	index := s.reads
	if index >= len(s.snapshots) {
		index = len(s.snapshots) - 1
	}
	s.reads++
	return s.snapshots[index], nil
}

type deviceApplyResponse struct {
	result domain.DeviceLabelApplyResult
	err    error
}

type postgresStateError string

func (e postgresStateError) Error() string    { return "postgres error" }
func (e postgresStateError) SQLState() string { return string(e) }

type reconciliationDeviceStub struct {
	snapshots []domain.DeviceLabelSnapshot
	gets      int
	applies   []deviceApplyResponse
	applyArgs []map[string]domain.DesiredDeviceLabel
}

func (s *reconciliationDeviceStub) GetLabelSnapshot(context.Context, uuid.UUID, string) (domain.DeviceLabelSnapshot, error) {
	index := s.gets
	if index >= len(s.snapshots) {
		index = len(s.snapshots) - 1
	}
	s.gets++
	return s.snapshots[index], nil
}

func (*reconciliationDeviceStub) GetLabelSyncMappingIDsByKeys(context.Context, uuid.UUID, []string) (map[string][]uuid.UUID, error) {
	return map[string][]uuid.UUID{}, nil
}

func (s *reconciliationDeviceStub) ApplyLabels(_ context.Context, _ uuid.UUID, _ string, _ domain.DeviceLabelSnapshot, desired map[string]domain.DesiredDeviceLabel) (domain.DeviceLabelApplyResult, error) {
	cloned := make(map[string]domain.DesiredDeviceLabel, len(desired))
	for key, label := range desired {
		cloned[key] = label
	}
	s.applyArgs = append(s.applyArgs, cloned)
	index := len(s.applyArgs) - 1
	if index >= len(s.applies) {
		return domain.DeviceLabelApplyResult{}, nil
	}
	response := s.applies[index]
	return response.result, response.err
}

type reconciliationEventsStub struct{}

func (reconciliationEventsStub) CreateEvent(context.Context, uuid.UUID, *domain.Event) {}

func (reconciliationEventsStub) HandleGenericResourceDeletedEvents(context.Context, domain.ResourceKind, uuid.UUID, string, interface{}, interface{}, bool, error) {
}

func newReconcilerService(t *testing.T, mappingStore labelsyncmappingstore.Store, devices ReconciliationDeviceStore, evaluator Evaluator) *ServiceHandler {
	t.Helper()
	service, err := NewServiceHandler(mappingStore, devices, evaluator, eventservice.Service(reconciliationEventsStub{}), logrus.New())
	require.NoError(t, err)
	return service
}

func TestDesiredDeviceLabelsPreservesUserLabelsAndTakesOverMatchingKeys(t *testing.T) {
	takeoverID := uuid.New()
	missingID := uuid.New()
	retiredKey := "retired"
	takeover := testDeviceMapping(takeoverID, "takeover", "map-result", nil)
	missing := testDeviceMapping(missingID, "missing", "missing-result", &retiredKey)
	deviceSnapshot := deviceLabelSnapshot("edge-01", "1", map[string]string{
		"manual":   "preserve",
		"takeover": "user-value",
		"retired":  "old",
	}, []domain.DeviceLabelOwnership{{Key: "retired", Value: "old", MappingID: &missingID}})
	snapshot := deviceReconciliationSnapshot{
		Device:   deviceSnapshot,
		Mappings: []labelsyncmappingstore.DeviceMapping{takeover, missing},
	}
	evaluator := &reconciliationEvaluator{responses: map[string]evaluatorResponse{
		"map-result":     {result: MapResult{"takeover": "mapped-value"}},
		"missing-result": {result: NoResult{}},
	}}

	desired, outcomes, err := desiredDeviceLabels(snapshot, evaluator)
	require.NoError(t, err)
	assert.Equal(t, map[string]domain.DesiredDeviceLabel{
		"manual":   {Value: "preserve"},
		"takeover": {Value: "mapped-value", MappingID: &takeoverID},
	}, desired)
	assert.Len(t, outcomes, 2)
	assert.NoError(t, outcomes[0].Err)
	assert.NoError(t, outcomes[1].Err)
}

func TestDesiredDeviceLabelsRetainsAllPreviousOutputsWhenMapEvaluationFails(t *testing.T) {
	mappingID := uuid.New()
	mapping := testDeviceMapping(mappingID, "map", "invalid-map", nil)
	snapshot := deviceReconciliationSnapshot{
		Device: deviceLabelSnapshot("edge-01", "1", map[string]string{
			"first":  "previous-one",
			"second": "previous-two",
			"manual": "preserve",
		}, []domain.DeviceLabelOwnership{
			{Key: "first", Value: "previous-one", MappingID: &mappingID},
			{Key: "second", Value: "previous-two", MappingID: &mappingID},
		}),
		Mappings: []labelsyncmappingstore.DeviceMapping{mapping},
	}
	evaluator := &reconciliationEvaluator{responses: map[string]evaluatorResponse{
		"invalid-map": {result: MapResult{"first": "partial"}, err: errors.New("invalid entry")},
	}}

	desired, outcomes, err := desiredDeviceLabels(snapshot, evaluator)
	require.NoError(t, err)
	assert.Equal(t, map[string]domain.DesiredDeviceLabel{
		"first":  {Value: "previous-one", MappingID: &mappingID},
		"second": {Value: "previous-two", MappingID: &mappingID},
		"manual": {Value: "preserve"},
	}, desired)
	require.Len(t, outcomes, 1)
	assert.Error(t, outcomes[0].Err)
}

func TestDesiredDeviceLabelsDiscardsAllOutputsWhenAnyMapKeyConflicts(t *testing.T) {
	firstID := uuid.New()
	secondID := uuid.New()
	first := testDeviceMapping(firstID, "first", "first-map", nil)
	second := testDeviceMapping(secondID, "second", "second-map", nil)
	snapshot := deviceReconciliationSnapshot{
		Device: deviceLabelSnapshot("edge-01", "1", map[string]string{
			"owned-first": "previous-one",
			"owned-last":  "previous-two",
			"manual":      "preserve",
		}, []domain.DeviceLabelOwnership{
			{Key: "owned-first", Value: "previous-one", MappingID: &firstID},
			{Key: "owned-last", Value: "previous-two", MappingID: &firstID},
		}),
		Mappings: []labelsyncmappingstore.DeviceMapping{first, second},
	}
	evaluator := &reconciliationEvaluator{responses: map[string]evaluatorResponse{
		"first-map":  {result: MapResult{"a-conflict": "first", "z-new": "discard"}},
		"second-map": {result: MapResult{"a-conflict": "second"}},
	}}

	desired, outcomes, err := desiredDeviceLabels(snapshot, evaluator)
	require.NoError(t, err)
	assert.Equal(t, map[string]domain.DesiredDeviceLabel{
		"owned-first": {Value: "previous-one", MappingID: &firstID},
		"owned-last":  {Value: "previous-two", MappingID: &firstID},
		"manual":      {Value: "preserve"},
	}, desired)
	require.Len(t, outcomes, 2)
	assert.Error(t, outcomes[0].Err)
	assert.Error(t, outcomes[1].Err)
}

func TestReconcileDeviceLabelsRetriesDeviceVersionConflict(t *testing.T) {
	orgID := uuid.New()
	mappingID := uuid.New()
	mapping := testDeviceMapping(mappingID, "architecture", "mapped", loPtr("architecture"))
	mappingStore := &reconciliationStoreStub{snapshots: []labelsyncmappingstore.DeviceMappingsSnapshot{{Mappings: []labelsyncmappingstore.DeviceMapping{mapping}, Revision: 1}}}
	snapshot := deviceLabelSnapshot("edge-01", "1", map[string]string{"manual": "keep"}, nil)
	devices := &reconciliationDeviceStub{
		snapshots: []domain.DeviceLabelSnapshot{snapshot},
		applies: []deviceApplyResponse{
			{err: flterrors.ErrResourceVersionConflict},
			{result: domain.DeviceLabelApplyResult{LabelsChanged: true}},
		},
	}
	evaluator := &reconciliationEvaluator{responses: map[string]evaluatorResponse{"mapped": {result: ScalarResult("x86_64")}}}
	service := newReconcilerService(t, mappingStore, devices, evaluator)

	result, err := service.ReconcileDeviceLabels(context.Background(), orgID, "edge-01")
	require.NoError(t, err)
	assert.True(t, result.LabelsChanged)
	assert.Len(t, devices.applyArgs, 2)
	assert.Equal(t, 3, mappingStore.reads)
	assert.Equal(t, []string{"mapped", "mapped"}, evaluator.called)
	assert.Equal(t, domain.DesiredDeviceLabel{Value: "x86_64", MappingID: &mappingID}, devices.applyArgs[0]["architecture"])
}

func TestReconcileDeviceLabelsSkipsDecommissionedDevice(t *testing.T) {
	orgID := uuid.New()
	mappingID := uuid.New()
	mapping := testDeviceMapping(mappingID, "architecture", "mapped", loPtr("architecture"))
	mappingStore := &reconciliationStoreStub{snapshots: []labelsyncmappingstore.DeviceMappingsSnapshot{{Mappings: []labelsyncmappingstore.DeviceMapping{mapping}, Revision: 1}}}
	decommissioning := domain.DeviceDecommission{}
	snapshot := deviceLabelSnapshot("edge-01", "2", map[string]string{}, nil)
	snapshot.Device.Spec = &domain.DeviceSpec{Decommissioning: &decommissioning}
	snapshot.Device.Metadata.Labels = nil
	devices := &reconciliationDeviceStub{snapshots: []domain.DeviceLabelSnapshot{snapshot}}
	evaluator := &reconciliationEvaluator{responses: map[string]evaluatorResponse{"mapped": {result: ScalarResult("x86_64")}}}
	service := newReconcilerService(t, mappingStore, devices, evaluator)

	result, err := service.ReconcileDeviceLabels(context.Background(), orgID, "edge-01")
	require.NoError(t, err)
	assert.Equal(t, ReconciliationResult{}, result)
	assert.Empty(t, evaluator.called)
	assert.Empty(t, devices.applyArgs)
}

func TestReconcileDeviceLabelsStopsAfterDecommissionWinsSnapshotRace(t *testing.T) {
	orgID := uuid.New()
	mappingID := uuid.New()
	mapping := testDeviceMapping(mappingID, "architecture", "mapped", loPtr("architecture"))
	mappingStore := &reconciliationStoreStub{snapshots: []labelsyncmappingstore.DeviceMappingsSnapshot{{Mappings: []labelsyncmappingstore.DeviceMapping{mapping}, Revision: 1}}}
	activeSnapshot := deviceLabelSnapshot("edge-01", "1", map[string]string{"manual": "keep"}, nil)
	decommissioning := domain.DeviceDecommission{}
	decommissionedSnapshot := deviceLabelSnapshot("edge-01", "2", map[string]string{}, nil)
	decommissionedSnapshot.Device.Spec = &domain.DeviceSpec{Decommissioning: &decommissioning}
	decommissionedSnapshot.Device.Metadata.Labels = nil
	devices := &reconciliationDeviceStub{
		snapshots: []domain.DeviceLabelSnapshot{activeSnapshot, decommissionedSnapshot},
		applies:   []deviceApplyResponse{{err: flterrors.ErrResourceVersionConflict}},
	}
	evaluator := &reconciliationEvaluator{responses: map[string]evaluatorResponse{"mapped": {result: ScalarResult("x86_64")}}}
	service := newReconcilerService(t, mappingStore, devices, evaluator)

	result, err := service.ReconcileDeviceLabels(context.Background(), orgID, "edge-01")
	require.NoError(t, err)
	assert.Equal(t, ReconciliationResult{}, result)
	assert.Equal(t, []string{"mapped"}, evaluator.called)
	assert.Len(t, devices.applyArgs, 1)
	assert.Equal(t, 2, devices.gets)
}

func TestReconcileDeviceLabelsSkipsDeviceStoreWhenNoMappings(t *testing.T) {
	orgID := uuid.New()
	mappingStore := &reconciliationStoreStub{snapshots: []labelsyncmappingstore.DeviceMappingsSnapshot{{}}}
	devices := &reconciliationDeviceStub{}
	service := newReconcilerService(t, mappingStore, devices, &reconciliationEvaluator{})

	result, err := service.ReconcileDeviceLabels(context.Background(), orgID, "edge-01")

	require.NoError(t, err)
	assert.Empty(t, result.MappingOutcomes)
	assert.Equal(t, 1, mappingStore.reads)
	assert.Zero(t, devices.gets)
	assert.Empty(t, devices.applyArgs)
}

func TestReconcileDeviceLabelsRetriesDatabaseDeadlock(t *testing.T) {
	orgID := uuid.New()
	mappingID := uuid.New()
	mapping := testDeviceMapping(mappingID, "architecture", "mapped", loPtr("architecture"))
	mappingStore := &reconciliationStoreStub{snapshots: []labelsyncmappingstore.DeviceMappingsSnapshot{{Mappings: []labelsyncmappingstore.DeviceMapping{mapping}, Revision: 1}}}
	snapshot := deviceLabelSnapshot("edge-01", "1", map[string]string{}, nil)
	devices := &reconciliationDeviceStub{
		snapshots: []domain.DeviceLabelSnapshot{snapshot},
		applies: []deviceApplyResponse{
			{err: postgresStateError("40P01")},
			{result: domain.DeviceLabelApplyResult{LabelsChanged: true}},
		},
	}
	evaluator := &reconciliationEvaluator{responses: map[string]evaluatorResponse{"mapped": {result: ScalarResult("x86_64")}}}
	service := newReconcilerService(t, mappingStore, devices, evaluator)

	result, err := service.ReconcileDeviceLabels(context.Background(), orgID, "edge-01")
	require.NoError(t, err)
	assert.True(t, result.LabelsChanged)
	assert.Len(t, devices.applyArgs, 2)
	assert.Equal(t, 3, mappingStore.reads)
}

func TestReconcileDeviceLabelsRetriesWhenMappingRevisionChangesAfterWrite(t *testing.T) {
	orgID := uuid.New()
	mappingID := uuid.New()
	mapping := testDeviceMapping(mappingID, "architecture", "mapped", loPtr("architecture"))
	mappingStore := &reconciliationStoreStub{snapshots: []labelsyncmappingstore.DeviceMappingsSnapshot{
		{Mappings: []labelsyncmappingstore.DeviceMapping{mapping}, Revision: 1},
		{Mappings: []labelsyncmappingstore.DeviceMapping{mapping}, Revision: 2},
		{Mappings: []labelsyncmappingstore.DeviceMapping{mapping}, Revision: 2},
		{Mappings: []labelsyncmappingstore.DeviceMapping{mapping}, Revision: 2},
	}}
	snapshot := deviceLabelSnapshot("edge-01", "1", map[string]string{}, nil)
	devices := &reconciliationDeviceStub{
		snapshots: []domain.DeviceLabelSnapshot{snapshot},
		applies: []deviceApplyResponse{
			{result: domain.DeviceLabelApplyResult{LabelsChanged: true, ManagedLabelsChanged: true, OwnershipChanged: true}},
			{result: domain.DeviceLabelApplyResult{}},
		},
	}
	evaluator := &reconciliationEvaluator{responses: map[string]evaluatorResponse{"mapped": {result: ScalarResult("x86_64")}}}
	service := newReconcilerService(t, mappingStore, devices, evaluator)

	result, err := service.ReconcileDeviceLabels(context.Background(), orgID, "edge-01")
	require.NoError(t, err)
	assert.Len(t, devices.applyArgs, 2)
	assert.True(t, result.LabelsChanged)
	assert.True(t, result.ManagedLabelsChanged)
	assert.True(t, result.OwnershipChanged)
	assert.Len(t, result.MappingOutcomes, 1)
	assert.Equal(t, mappingID, result.MappingOutcomes[0].MappingID)
	assert.Equal(t, 4, mappingStore.reads)
}

func TestReconcileDeviceLabelsReturnsFailureOutcomesWhenMappingRevisionKeepsChanging(t *testing.T) {
	orgID := uuid.New()
	mappingID := uuid.New()
	latestMappingID := uuid.New()
	mapping := testDeviceMapping(mappingID, "architecture", "mapped", loPtr("architecture"))
	latestMapping := testDeviceMapping(latestMappingID, "architecture", "mapped", loPtr("architecture"))
	generation := int64(1)
	resourceVersion := "snapshot-rv"
	mapping.Mapping.Metadata.Generation = &generation
	mapping.Mapping.Metadata.ResourceVersion = &resourceVersion
	latestGeneration := int64(2)
	latestResourceVersion := "latest-rv"
	latestMapping.Mapping.Metadata.Generation = &latestGeneration
	latestMapping.Mapping.Metadata.ResourceVersion = &latestResourceVersion

	mappingSnapshots := make([]labelsyncmappingstore.DeviceMappingsSnapshot, 0, maxReconciliationAttempts*2)
	for attempt := 0; attempt < maxReconciliationAttempts; attempt++ {
		mappingSnapshots = append(mappingSnapshots,
			labelsyncmappingstore.DeviceMappingsSnapshot{Mappings: []labelsyncmappingstore.DeviceMapping{mapping}, Revision: int64(attempt*2 + 1)},
			labelsyncmappingstore.DeviceMappingsSnapshot{Mappings: []labelsyncmappingstore.DeviceMapping{latestMapping}, Revision: int64(attempt*2 + 2)},
		)
	}
	mappingStore := &reconciliationStoreStub{snapshots: mappingSnapshots}
	devices := &reconciliationDeviceStub{snapshots: []domain.DeviceLabelSnapshot{deviceLabelSnapshot("edge-01", "1", map[string]string{}, nil)}}
	evaluator := &reconciliationEvaluator{responses: map[string]evaluatorResponse{"mapped": {result: ScalarResult("x86_64")}}}
	service := newReconcilerService(t, mappingStore, devices, evaluator)

	result, err := service.ReconcileDeviceLabels(context.Background(), orgID, "edge-01")

	require.ErrorIs(t, err, flterrors.ErrResourceVersionConflict)
	require.Len(t, result.MappingOutcomes, 1)
	assert.Equal(t, mappingID, result.MappingOutcomes[0].MappingID)
	assert.Equal(t, generation, result.MappingOutcomes[0].Generation)
	assert.Equal(t, resourceVersion, result.MappingOutcomes[0].ResourceVersion)
	assert.ErrorIs(t, result.MappingOutcomes[0].Err, flterrors.ErrResourceVersionConflict)
	assert.Len(t, devices.applyArgs, maxReconciliationAttempts)
	assert.Equal(t, maxReconciliationAttempts*2, mappingStore.reads)
}

func TestReconcileDeviceLabelsDoesNotRepeatOwnerOnlyTransfer(t *testing.T) {
	orgID := uuid.New()
	mappingID := uuid.New()
	mapping := testDeviceMapping(mappingID, "architecture", "mapped", loPtr("architecture"))
	storeSnapshot := labelsyncmappingstore.DeviceMappingsSnapshot{Mappings: []labelsyncmappingstore.DeviceMapping{mapping}, Revision: 1}
	mappingStore := &reconciliationStoreStub{snapshots: []labelsyncmappingstore.DeviceMappingsSnapshot{storeSnapshot}}
	labels := map[string]string{"architecture": "x86_64"}
	snapshotOne := deviceLabelSnapshot("edge-01", "1", labels, []domain.DeviceLabelOwnership{{Key: "architecture", Value: "x86_64"}})
	devices := &reconciliationDeviceStub{
		snapshots: []domain.DeviceLabelSnapshot{snapshotOne},
		applies:   []deviceApplyResponse{{result: domain.DeviceLabelApplyResult{OwnershipChanged: true}}},
	}
	evaluator := &reconciliationEvaluator{responses: map[string]evaluatorResponse{"mapped": {result: ScalarResult("x86_64")}}}
	service := newReconcilerService(t, mappingStore, devices, evaluator)

	_, err := service.ReconcileDeviceLabels(context.Background(), orgID, "edge-01")
	require.NoError(t, err)
	assert.Len(t, devices.applyArgs, 1)
	assert.Equal(t, 1, devices.gets)
}

func testDeviceMapping(id uuid.UUID, name, expression string, key *string) labelsyncmappingstore.DeviceMapping {
	return labelsyncmappingstore.DeviceMapping{
		ID: id,
		Mapping: domain.LabelSyncMapping{
			Metadata: domain.ObjectMeta{Name: &name},
			Spec: domain.LabelSyncMappingSpec{
				ResourceType: domain.LabelSyncMappingDevice,
				Key:          key,
				Expression:   expression,
			},
		},
	}
}

func deviceLabelSnapshot(name, resourceVersion string, labels map[string]string, owners []domain.DeviceLabelOwnership) domain.DeviceLabelSnapshot {
	return domain.DeviceLabelSnapshot{
		Device: domain.Device{Metadata: domain.ObjectMeta{
			Name:            &name,
			ResourceVersion: &resourceVersion,
			Labels:          &labels,
		}},
		Labels: owners,
	}
}

func loPtr(value string) *string { return &value }
