package labelsyncmapping

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	eventservice "github.com/flightctl/flightctl/internal/service/events"
	labelsyncmappingstore "github.com/flightctl/flightctl/internal/store/labelsyncmapping"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

type provenanceMappingStoreStub struct {
	labelsyncmappingstore.Store
	snapshot labelsyncmappingstore.DeviceMappingsSnapshot
	err      error
	orgIDs   []uuid.UUID
}

func (s *provenanceMappingStoreStub) GetDeviceMappingsSnapshot(_ context.Context, orgID uuid.UUID) (labelsyncmappingstore.DeviceMappingsSnapshot, error) {
	s.orgIDs = append(s.orgIDs, orgID)
	return s.snapshot, s.err
}

type provenanceDeviceStoreStub struct {
	ReconciliationDeviceStore
	snapshot       domain.DeviceLabelSnapshot
	snapshotErr    error
	ownersByKey    map[string][]uuid.UUID
	ownersErr      error
	snapshotOrgIDs []uuid.UUID
	ownerOrgIDs    []uuid.UUID
	queriedKeys    [][]string
}

func (s *provenanceDeviceStoreStub) GetLabelSnapshot(_ context.Context, orgID uuid.UUID, _ string) (domain.DeviceLabelSnapshot, error) {
	s.snapshotOrgIDs = append(s.snapshotOrgIDs, orgID)
	return s.snapshot, s.snapshotErr
}

func (s *provenanceDeviceStoreStub) GetLabelSyncMappingIDsByKeys(_ context.Context, orgID uuid.UUID, keys []string) (map[string][]uuid.UUID, error) {
	s.ownerOrgIDs = append(s.ownerOrgIDs, orgID)
	s.queriedKeys = append(s.queriedKeys, append([]string(nil), keys...))
	return s.ownersByKey, s.ownersErr
}

func newProvenanceService(t *testing.T, mappingStore *provenanceMappingStoreStub, devices *provenanceDeviceStoreStub) *ServiceHandler {
	t.Helper()
	service, err := NewServiceHandler(mappingStore, devices, handlerTestEvaluator{}, eventservice.NewMockService(gomock.NewController(t)), nil)
	require.NoError(t, err)
	return service
}

func provenanceMapping(id uuid.UUID, name string, deleting bool) labelsyncmappingstore.DeviceMapping {
	mapping := domain.LabelSyncMapping{Metadata: domain.ObjectMeta{Name: &name}}
	if deleting {
		deletionTimestamp := time.Unix(0, 0)
		mapping.Metadata.DeletionTimestamp = &deletionTimestamp
	}
	return labelsyncmappingstore.DeviceMapping{ID: id, Mapping: mapping}
}

func TestGetDeviceLabelSyncProvenance(t *testing.T) {
	orgID := uuid.New()
	mappingID := uuid.New()
	staleMappingID := uuid.New()
	mappingStore := &provenanceMappingStoreStub{snapshot: labelsyncmappingstore.DeviceMappingsSnapshot{
		Mappings: []labelsyncmappingstore.DeviceMapping{provenanceMapping(mappingID, "device-facts", false)},
	}}
	devices := &provenanceDeviceStoreStub{snapshot: domain.DeviceLabelSnapshot{Labels: []domain.DeviceLabelOwnership{
		{Key: "hardware.architecture", Value: "x86_64", MappingID: &mappingID},
		{Key: "operator.label", Value: "manual"},
		{Key: "stale.label", Value: "old", MappingID: &staleMappingID},
	}}}
	service := newProvenanceService(t, mappingStore, devices)

	result, status := service.GetDeviceLabelSyncProvenance(context.Background(), orgID, "edge-01")

	require.EqualValues(t, 200, status.Code)
	require.NotNil(t, result)
	assert.Equal(t, []domain.LabelSyncProvenanceItem{{Key: "hardware.architecture", Owners: []string{"device-facts"}}}, result.Items)
	assert.Equal(t, []uuid.UUID{orgID}, devices.snapshotOrgIDs)
	assert.Equal(t, []uuid.UUID{orgID}, mappingStore.orgIDs)
}

func TestGetDeviceLabelSyncProvenanceEmptyAndMissing(t *testing.T) {
	t.Run("When an existing device has no current mapping-owned labels it should return an empty list", func(t *testing.T) {
		service := newProvenanceService(t, &provenanceMappingStoreStub{}, &provenanceDeviceStoreStub{})

		result, status := service.GetDeviceLabelSyncProvenance(context.Background(), uuid.New(), "edge-01")

		require.EqualValues(t, 200, status.Code)
		require.NotNil(t, result)
		assert.NotNil(t, result.Items)
		assert.Empty(t, result.Items)
	})

	t.Run("When the device does not exist it should return not found", func(t *testing.T) {
		devices := &provenanceDeviceStoreStub{snapshotErr: flterrors.ErrResourceNotFound}
		service := newProvenanceService(t, &provenanceMappingStoreStub{}, devices)

		result, status := service.GetDeviceLabelSyncProvenance(context.Background(), uuid.New(), "missing")

		assert.Nil(t, result)
		assert.EqualValues(t, 404, status.Code)
	})
}

func TestGetLabelSyncProvenance(t *testing.T) {
	orgID := uuid.New()
	alphaID := uuid.New()
	betaID := uuid.New()
	deletingID := uuid.New()
	staleID := uuid.New()
	mappingStore := &provenanceMappingStoreStub{snapshot: labelsyncmappingstore.DeviceMappingsSnapshot{Mappings: []labelsyncmappingstore.DeviceMapping{
		provenanceMapping(betaID, "beta", false),
		provenanceMapping(alphaID, "alpha", false),
		provenanceMapping(deletingID, "deleting", true),
	}}}
	devices := &provenanceDeviceStoreStub{ownersByKey: map[string][]uuid.UUID{
		"shared.key": {betaID, alphaID, betaID},
		"stale.key":  {staleID, deletingID},
	}}
	service := newProvenanceService(t, mappingStore, devices)
	keys := []string{"shared.key", "unowned.key", "stale.key", "shared.key"}

	result, status := service.GetLabelSyncProvenance(context.Background(), orgID, keys)

	require.EqualValues(t, 200, status.Code)
	require.NotNil(t, result)
	assert.Equal(t, []domain.LabelSyncProvenanceItem{
		{Key: "shared.key", Owners: []string{"alpha", "beta"}},
		{Key: "unowned.key", Owners: []string{}},
		{Key: "stale.key", Owners: []string{}},
		{Key: "shared.key", Owners: []string{"alpha", "beta"}},
	}, result.Items)
	assert.Equal(t, []uuid.UUID{orgID}, mappingStore.orgIDs)
	assert.Equal(t, []uuid.UUID{orgID}, devices.ownerOrgIDs)
	assert.Equal(t, [][]string{keys}, devices.queriedKeys)
}

func TestGetLabelSyncProvenanceRejectsInvalidRequests(t *testing.T) {
	tests := []struct {
		name string
		keys []string
	}{
		{name: "When no keys are supplied it should reject the request"},
		{name: "When more than 50 keys are supplied it should reject the request", keys: make([]string, 51)},
		{name: "When an empty key is supplied it should reject the request", keys: []string{"key", ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			devices := &provenanceDeviceStoreStub{}
			service := newProvenanceService(t, &provenanceMappingStoreStub{}, devices)

			result, status := service.GetLabelSyncProvenance(context.Background(), uuid.New(), tt.keys)

			assert.Nil(t, result)
			assert.EqualValues(t, 400, status.Code)
			assert.Empty(t, devices.ownerOrgIDs)
		})
	}
}

func TestGetLabelSyncProvenanceReturnsStoreErrors(t *testing.T) {
	t.Run("When mapping ownership cannot be read it should return an internal error", func(t *testing.T) {
		devices := &provenanceDeviceStoreStub{ownersErr: errors.New("query failed")}
		service := newProvenanceService(t, &provenanceMappingStoreStub{}, devices)

		result, status := service.GetLabelSyncProvenance(context.Background(), uuid.New(), []string{"key"})

		assert.Nil(t, result)
		assert.EqualValues(t, 500, status.Code)
	})

	t.Run("When active mappings cannot be read it should return an internal error", func(t *testing.T) {
		mappingStore := &provenanceMappingStoreStub{err: errors.New("query failed")}
		service := newProvenanceService(t, mappingStore, &provenanceDeviceStoreStub{})

		result, status := service.GetDeviceLabelSyncProvenance(context.Background(), uuid.New(), "edge-01")

		assert.Nil(t, result)
		assert.EqualValues(t, 500, status.Code)
	})
}
