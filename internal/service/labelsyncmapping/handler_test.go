package labelsyncmapping

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	eventservice "github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/store"
	labelsyncmappingstore "github.com/flightctl/flightctl/internal/store/labelsyncmapping"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeStore struct {
	mappings              map[uuid.UUID]map[string]*domain.LabelSyncMapping
	deviceLabelReferences map[uuid.UUID]map[string]int
	deleteErr             error
	finalizeDeleteErr     error
	finalizeDeleteCalls   int
}

type handlerTestEvaluator struct{ validationErr error }

func (e handlerTestEvaluator) ValidateExpressionIs(string, ResultKind) error {
	return e.validationErr
}

func (handlerTestEvaluator) Evaluate(string, Activation) (Result, error) {
	return NoResult{}, nil
}

func newCRUDServiceHandler(t *testing.T, store labelsyncmappingstore.Store, evaluator Evaluator) *ServiceHandler {
	t.Helper()
	handler, err := NewServiceHandler(store, &reconciliationDeviceStub{}, evaluator, eventservice.Service(reconciliationEventsStub{}), nil)
	require.NoError(t, err)
	return handler
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		mappings:              make(map[uuid.UUID]map[string]*domain.LabelSyncMapping),
		deviceLabelReferences: make(map[uuid.UUID]map[string]int),
	}
}

func (s *fakeStore) addDeviceLabelReference(orgID uuid.UUID, name string) {
	if s.deviceLabelReferences[orgID] == nil {
		s.deviceLabelReferences[orgID] = make(map[string]int)
	}
	s.deviceLabelReferences[orgID][name]++
}

func (*fakeStore) InitialMigration(context.Context) error { return nil }

func (*fakeStore) GetDeviceMappingsSnapshot(context.Context, uuid.UUID) (labelsyncmappingstore.DeviceMappingsSnapshot, error) {
	return labelsyncmappingstore.DeviceMappingsSnapshot{}, nil
}

func (s *fakeStore) Create(_ context.Context, orgID uuid.UUID, mapping *domain.LabelSyncMapping) (*domain.LabelSyncMapping, error) {
	if s.mappings[orgID] == nil {
		s.mappings[orgID] = make(map[string]*domain.LabelSyncMapping)
	}
	name := lo.FromPtr(mapping.Metadata.Name)
	if _, found := s.mappings[orgID][name]; found {
		return nil, flterrors.ErrDuplicateName
	}
	mapping.Metadata.Generation = lo.ToPtr(int64(1))
	mapping.Metadata.ResourceVersion = lo.ToPtr("1")
	s.mappings[orgID][name] = mapping
	return mapping, nil
}

func (s *fakeStore) Update(_ context.Context, orgID uuid.UUID, mapping *domain.LabelSyncMapping) (*domain.LabelSyncMapping, *domain.LabelSyncMapping, error) {
	name := lo.FromPtr(mapping.Metadata.Name)
	previous, found := s.mappings[orgID][name]
	if !found {
		return nil, nil, flterrors.ErrResourceNotFound
	}
	if !reflect.DeepEqual(mapping.Spec, previous.Spec) {
		mapping.Metadata.Generation = lo.ToPtr(lo.FromPtr(previous.Metadata.Generation) + 1)
	} else {
		mapping.Metadata.Generation = previous.Metadata.Generation
	}
	mapping.Metadata.ResourceVersion = lo.ToPtr("2")
	s.mappings[orgID][name] = mapping
	return mapping, previous, nil
}

func (s *fakeStore) Get(_ context.Context, orgID uuid.UUID, name string) (*domain.LabelSyncMapping, error) {
	mapping, found := s.mappings[orgID][name]
	if !found {
		return nil, flterrors.ErrResourceNotFound
	}
	return mapping, nil
}

func (s *fakeStore) List(_ context.Context, orgID uuid.UUID, _ store.ListParams) (*domain.LabelSyncMappingList, error) {
	list := &domain.LabelSyncMappingList{}
	for _, mapping := range s.mappings[orgID] {
		list.Items = append(list.Items, *mapping)
	}
	return list, nil
}

func (s *fakeStore) Delete(_ context.Context, orgID uuid.UUID, name string) (bool, error) {
	if s.deleteErr != nil {
		return false, s.deleteErr
	}
	mapping, found := s.mappings[orgID][name]
	if !found {
		return false, nil
	}
	now := time.Now()
	mapping.Metadata.DeletionTimestamp = &now
	mapping.Metadata.ResourceVersion = lo.ToPtr("3")
	mapping.Status = &domain.LabelSyncMappingStatus{Conditions: &[]domain.Condition{{
		Type:               domain.ConditionTypeLabelSyncMappingReady,
		Status:             domain.ConditionStatusFalse,
		Reason:             "Pending",
		ObservedGeneration: mapping.Metadata.Generation,
	}}}
	return true, nil
}

func (s *fakeStore) FinalizeDelete(_ context.Context, orgID uuid.UUID, name string) (bool, error) {
	s.finalizeDeleteCalls++
	if s.finalizeDeleteErr != nil {
		return false, s.finalizeDeleteErr
	}
	mapping, found := s.mappings[orgID][name]
	if !found || mapping.Metadata.DeletionTimestamp == nil {
		return false, nil
	}
	if s.deviceLabelReferences[orgID][name] > 0 {
		return false, nil
	}
	delete(s.mappings[orgID], name)
	return true, nil
}

func mapping(name string) domain.LabelSyncMapping {
	return domain.LabelSyncMapping{
		Metadata: domain.ObjectMeta{Name: &name},
		Spec: domain.LabelSyncMappingSpec{
			ResourceType: domain.LabelSyncMappingDevice,
			Key:          lo.ToPtr("architecture"),
			Expression:   "device.status.systemInfo.architecture",
		},
	}
}

func TestNewServiceHandlerRequiresDependencies(t *testing.T) {
	validStore := newFakeStore()
	validDeviceStore := &reconciliationDeviceStub{}
	validEvaluator := handlerTestEvaluator{}
	validEvents := eventservice.Service(reconciliationEventsStub{})
	tests := []struct {
		name        string
		store       labelsyncmappingstore.Store
		deviceStore ReconciliationDeviceStore
		evaluator   Evaluator
		events      eventservice.Service
		wantError   string
	}{
		{
			name:        "When the label-sync mapping store is missing it should reject construction",
			deviceStore: validDeviceStore,
			evaluator:   validEvaluator,
			events:      validEvents,
			wantError:   "label-sync mapping store is required",
		},
		{
			name:      "When the device store is missing it should reject construction",
			store:     validStore,
			evaluator: validEvaluator,
			events:    validEvents,
			wantError: "device store is required",
		},
		{
			name:        "When the evaluator is missing it should reject construction",
			store:       validStore,
			deviceStore: validDeviceStore,
			events:      validEvents,
			wantError:   "mapping evaluator is required",
		},
		{
			name:        "When the event service is missing it should reject construction",
			store:       validStore,
			deviceStore: validDeviceStore,
			evaluator:   validEvaluator,
			wantError:   "event service is required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewServiceHandler(tt.store, tt.deviceStore, tt.evaluator, tt.events, nil)
			require.ErrorContains(t, err, tt.wantError)
		})
	}
}

func TestLabelSyncMappingLifecycle(t *testing.T) {
	ctx := context.Background()
	firstOrg := uuid.New()
	secondOrg := uuid.New()
	store := newFakeStore()
	handler := newCRUDServiceHandler(t, store, handlerTestEvaluator{})

	t.Run("When the expression validator rejects a mapping it should return 422 without persisting", func(t *testing.T) {
		validationStore := newFakeStore()
		validationHandler := newCRUDServiceHandler(t, validationStore, handlerTestEvaluator{validationErr: errors.New("expression output does not match map mode")})
		created, status := validationHandler.CreateLabelSyncMapping(ctx, firstOrg, mapping("invalid-expression"))
		require.Nil(t, created)
		assert.EqualValues(t, 422, status.Code)
		assert.Contains(t, status.Message, "map mode")
		assert.Empty(t, validationStore.mappings[firstOrg])
	})

	t.Run("When a valid mapping is created it should begin pending propagation", func(t *testing.T) {
		created, status := handler.CreateLabelSyncMapping(ctx, firstOrg, mapping("architecture"))
		require.EqualValues(t, 201, status.Code)
		require.NotNil(t, created.Status)
		require.Len(t, lo.FromPtr(created.Status.Conditions), 1)
		condition := lo.FromPtr(created.Status.Conditions)[0]
		assert.Equal(t, domain.ConditionStatusFalse, condition.Status)
		assert.Equal(t, "Pending", condition.Reason)
		assert.EqualValues(t, 1, lo.FromPtr(condition.ObservedGeneration))
	})

	t.Run("When a mapping belongs to another organization it should not be visible", func(t *testing.T) {
		_, status := handler.GetLabelSyncMapping(ctx, secondOrg, "architecture")
		assert.EqualValues(t, 404, status.Code)
	})

	t.Run("When a mapping key is updated it should increment generation and return to pending", func(t *testing.T) {
		updated := mapping("architecture")
		updated.Spec.Key = lo.ToPtr("cpu-architecture")
		updated.Metadata.ResourceVersion = lo.ToPtr("1")
		result, status := handler.ReplaceLabelSyncMapping(ctx, firstOrg, "architecture", updated)
		require.EqualValues(t, 200, status.Code)
		assert.EqualValues(t, 2, lo.FromPtr(result.Metadata.Generation))
		condition := lo.FromPtr(result.Status.Conditions)[0]
		assert.EqualValues(t, 2, lo.FromPtr(condition.ObservedGeneration))
	})

	t.Run("When the resource type changes it should reject the update", func(t *testing.T) {
		updated := mapping("architecture")
		updated.Spec.ResourceType = "Fleet"
		_, status := handler.ReplaceLabelSyncMapping(ctx, firstOrg, "architecture", updated)
		assert.EqualValues(t, 400, status.Code)
	})

	t.Run("When metadata changes without a spec change it should preserve condition and generation", func(t *testing.T) {
		current := store.mappings[firstOrg]["architecture"]
		current.Status = &domain.LabelSyncMappingStatus{Conditions: &[]domain.Condition{{
			Type:               domain.ConditionTypeLabelSyncMappingReady,
			Status:             domain.ConditionStatusTrue,
			Reason:             "Success",
			ObservedGeneration: lo.ToPtr(int64(2)),
		}}}
		labels := map[string]string{"team": "edge"}
		updated := *current
		updated.Metadata.Labels = &labels
		result, status := handler.ReplaceLabelSyncMapping(ctx, firstOrg, "architecture", updated)
		require.EqualValues(t, 200, status.Code)
		assert.EqualValues(t, 2, lo.FromPtr(result.Metadata.Generation))
		condition := lo.FromPtr(result.Status.Conditions)[0]
		assert.Equal(t, "Success", condition.Reason)
		assert.Equal(t, domain.ConditionStatusTrue, condition.Status)
	})

	t.Run("When a mapping owns device labels it should remain readable during cleanup", func(t *testing.T) {
		store.addDeviceLabelReference(firstOrg, "architecture")
		status := handler.DeleteLabelSyncMapping(ctx, firstOrg, "architecture")
		require.EqualValues(t, 200, status.Code)
		assert.Equal(t, 1, store.finalizeDeleteCalls)
		deleted, status := handler.GetLabelSyncMapping(ctx, firstOrg, "architecture")
		require.EqualValues(t, 200, status.Code)
		assert.NotNil(t, deleted.Metadata.DeletionTimestamp)
		assert.Equal(t, "Pending", lo.FromPtr(deleted.Status.Conditions)[0].Reason)
	})

	t.Run("When an unowned mapping is deleted it should be removed during finalization", func(t *testing.T) {
		unownedStore := newFakeStore()
		unownedHandler := newCRUDServiceHandler(t, unownedStore, handlerTestEvaluator{})
		_, createStatus := unownedHandler.CreateLabelSyncMapping(ctx, firstOrg, mapping("unowned"))
		require.EqualValues(t, 201, createStatus.Code)

		status := unownedHandler.DeleteLabelSyncMapping(ctx, firstOrg, "unowned")
		assert.EqualValues(t, 200, status.Code)
		assert.Equal(t, 1, unownedStore.finalizeDeleteCalls)

		_, getStatus := unownedHandler.GetLabelSyncMapping(ctx, firstOrg, "unowned")
		assert.EqualValues(t, 404, getStatus.Code)
	})

	t.Run("When deletion fails it should not attempt finalization", func(t *testing.T) {
		deleteStore := newFakeStore()
		deleteStore.deleteErr = errors.New("delete failed")
		deleteHandler := newCRUDServiceHandler(t, deleteStore, handlerTestEvaluator{})

		status := deleteHandler.DeleteLabelSyncMapping(ctx, firstOrg, "architecture")
		assert.EqualValues(t, 500, status.Code)
		assert.Zero(t, deleteStore.finalizeDeleteCalls)
	})

	t.Run("When the mapping is not found it should not attempt finalization", func(t *testing.T) {
		missingStore := newFakeStore()
		missingHandler := newCRUDServiceHandler(t, missingStore, handlerTestEvaluator{})

		status := missingHandler.DeleteLabelSyncMapping(ctx, firstOrg, "missing")
		assert.EqualValues(t, 200, status.Code)
		assert.Zero(t, missingStore.finalizeDeleteCalls)
	})

	t.Run("When finalization fails it should return an internal server error", func(t *testing.T) {
		finalizeStore := newFakeStore()
		finalizeHandler := newCRUDServiceHandler(t, finalizeStore, handlerTestEvaluator{})
		_, createStatus := finalizeHandler.CreateLabelSyncMapping(ctx, firstOrg, mapping("finalize-failure"))
		require.EqualValues(t, 201, createStatus.Code)
		finalizeStore.finalizeDeleteErr = errors.New("finalization failed")

		status := finalizeHandler.DeleteLabelSyncMapping(ctx, firstOrg, "finalize-failure")
		assert.EqualValues(t, 500, status.Code)
		assert.Equal(t, 1, finalizeStore.finalizeDeleteCalls)
	})
}
