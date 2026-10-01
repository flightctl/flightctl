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
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

type handlerTestEvaluator struct{ validationErr error }

func (e handlerTestEvaluator) ValidateExpressionIs(string, ResultKind) error {
	return e.validationErr
}

func (handlerTestEvaluator) Evaluate(string, Activation) (Result, error) {
	return NoResult{}, nil
}

func newCRUDServiceHandler(t *testing.T, evaluator Evaluator) (*ServiceHandler, *labelsyncmappingstore.MockStore) {
	store := labelsyncmappingstore.NewMockStore(gomock.NewController(t))
	handler, err := NewServiceHandler(store, NewMockReconciliationDeviceStore(gomock.NewController(t)), evaluator, eventservice.NewMockService(gomock.NewController(t)), nil)
	require.NoError(t, err)
	return handler, store
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

func storedMapping(name string, generation int64, resourceVersion string, status *domain.LabelSyncMappingStatus) *domain.LabelSyncMapping {
	result := mapping(name)
	result.Metadata.Generation = lo.ToPtr(generation)
	result.Metadata.ResourceVersion = lo.ToPtr(resourceVersion)
	result.Status = status
	return &result
}

func TestNewServiceHandlerRequiresDependencies(t *testing.T) {
	validStore := labelsyncmappingstore.NewMockStore(gomock.NewController(t))
	validDeviceStore := NewMockReconciliationDeviceStore(gomock.NewController(t))
	validEvaluator := handlerTestEvaluator{}
	validEvents := eventservice.NewMockService(gomock.NewController(t))
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

	t.Run("When the expression validator rejects a mapping it should return 422 without persisting", func(t *testing.T) {
		handler, _ := newCRUDServiceHandler(t, handlerTestEvaluator{validationErr: errors.New("expression output does not match map mode")})
		created, status := handler.CreateLabelSyncMapping(ctx, firstOrg, mapping("invalid-expression"))
		require.Nil(t, created)
		assert.EqualValues(t, 422, status.Code)
		assert.Contains(t, status.Message, "map mode")
	})

	t.Run("When a valid mapping is created it should begin pending propagation", func(t *testing.T) {
		handler, store := newCRUDServiceHandler(t, handlerTestEvaluator{})
		store.EXPECT().Create(gomock.Any(), firstOrg, gomock.Any()).DoAndReturn(func(_ context.Context, _ uuid.UUID, created *domain.LabelSyncMapping) (*domain.LabelSyncMapping, error) {
			result := *created
			result.Metadata.Generation = lo.ToPtr(int64(1))
			result.Metadata.ResourceVersion = lo.ToPtr("1")
			return &result, nil
		})
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
		handler, store := newCRUDServiceHandler(t, handlerTestEvaluator{})
		store.EXPECT().Get(gomock.Any(), secondOrg, "architecture").Return(nil, flterrors.ErrResourceNotFound)
		_, status := handler.GetLabelSyncMapping(ctx, secondOrg, "architecture")
		assert.EqualValues(t, 404, status.Code)
	})

	t.Run("When a mapping key is updated it should increment generation and return to pending", func(t *testing.T) {
		handler, store := newCRUDServiceHandler(t, handlerTestEvaluator{})
		current := storedMapping("architecture", 1, "1", &domain.LabelSyncMappingStatus{})
		store.EXPECT().Get(gomock.Any(), firstOrg, "architecture").Return(current, nil)
		store.EXPECT().Update(gomock.Any(), firstOrg, gomock.Any()).DoAndReturn(func(_ context.Context, _ uuid.UUID, updated *domain.LabelSyncMapping) (*domain.LabelSyncMapping, *domain.LabelSyncMapping, error) {
			result := *updated
			result.Metadata.Generation = lo.ToPtr(int64(2))
			result.Metadata.ResourceVersion = lo.ToPtr("2")
			return &result, current, nil
		})
		updated := mapping("architecture")
		updated.Spec.Key = lo.ToPtr("cpu-architecture")
		result, status := handler.ReplaceLabelSyncMapping(ctx, firstOrg, "architecture", updated)
		require.EqualValues(t, 200, status.Code)
		assert.EqualValues(t, 2, lo.FromPtr(result.Metadata.Generation))
		assert.EqualValues(t, 2, lo.FromPtr(lo.FromPtr(result.Status.Conditions)[0].ObservedGeneration))
	})

	t.Run("When the resource type changes it should reject the update", func(t *testing.T) {
		handler, store := newCRUDServiceHandler(t, handlerTestEvaluator{})
		current := storedMapping("architecture", 1, "1", &domain.LabelSyncMappingStatus{})
		store.EXPECT().Get(gomock.Any(), firstOrg, "architecture").Return(current, nil)
		updated := mapping("architecture")
		updated.Spec.ResourceType = "Fleet"
		_, status := handler.ReplaceLabelSyncMapping(ctx, firstOrg, "architecture", updated)
		assert.EqualValues(t, 400, status.Code)
	})

	t.Run("When metadata changes without a spec change it should preserve condition and generation", func(t *testing.T) {
		handler, store := newCRUDServiceHandler(t, handlerTestEvaluator{})
		current := storedMapping("architecture", 2, "2", &domain.LabelSyncMappingStatus{Conditions: &[]domain.Condition{{
			Type: domain.ConditionTypeLabelSyncMappingReady, Status: domain.ConditionStatusTrue, Reason: "Success", ObservedGeneration: lo.ToPtr(int64(2)),
		}}})
		store.EXPECT().Get(gomock.Any(), firstOrg, "architecture").Return(current, nil)
		store.EXPECT().Update(gomock.Any(), firstOrg, gomock.Any()).DoAndReturn(func(_ context.Context, _ uuid.UUID, updated *domain.LabelSyncMapping) (*domain.LabelSyncMapping, *domain.LabelSyncMapping, error) {
			result := *updated
			result.Metadata.ResourceVersion = lo.ToPtr("3")
			return &result, current, nil
		})
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
		handler, store := newCRUDServiceHandler(t, handlerTestEvaluator{})
		terminating := storedMapping("architecture", 1, "2", nil)
		now := time.Now()
		terminating.Metadata.DeletionTimestamp = &now
		terminating.Status = &domain.LabelSyncMappingStatus{Conditions: &[]domain.Condition{{
			Type: domain.ConditionTypeLabelSyncMappingReady, Status: domain.ConditionStatusFalse, Reason: "Pending", ObservedGeneration: lo.ToPtr(int64(1)),
		}}}
		store.EXPECT().Delete(gomock.Any(), firstOrg, "architecture").Return(true, nil)
		store.EXPECT().FinalizeDelete(gomock.Any(), firstOrg, "architecture").Return(false, nil)
		store.EXPECT().Get(gomock.Any(), firstOrg, "architecture").Return(terminating, nil)
		status := handler.DeleteLabelSyncMapping(ctx, firstOrg, "architecture")
		require.EqualValues(t, 200, status.Code)
		deleted, status := handler.GetLabelSyncMapping(ctx, firstOrg, "architecture")
		require.EqualValues(t, 200, status.Code)
		assert.NotNil(t, deleted.Metadata.DeletionTimestamp)
		assert.Equal(t, "Pending", lo.FromPtr(deleted.Status.Conditions)[0].Reason)
	})

	t.Run("When an unowned mapping is deleted it should be removed during finalization", func(t *testing.T) {
		handler, store := newCRUDServiceHandler(t, handlerTestEvaluator{})
		store.EXPECT().Delete(gomock.Any(), firstOrg, "unowned").Return(true, nil)
		store.EXPECT().FinalizeDelete(gomock.Any(), firstOrg, "unowned").Return(true, nil)
		status := handler.DeleteLabelSyncMapping(ctx, firstOrg, "unowned")
		assert.EqualValues(t, 200, status.Code)
		store.EXPECT().Get(gomock.Any(), firstOrg, "unowned").Return(nil, flterrors.ErrResourceNotFound)
		_, getStatus := handler.GetLabelSyncMapping(ctx, firstOrg, "unowned")
		assert.EqualValues(t, 404, getStatus.Code)
	})

	t.Run("When deletion fails it should not attempt finalization", func(t *testing.T) {
		handler, store := newCRUDServiceHandler(t, handlerTestEvaluator{})
		store.EXPECT().Delete(gomock.Any(), firstOrg, "architecture").Return(false, errors.New("delete failed"))
		status := handler.DeleteLabelSyncMapping(ctx, firstOrg, "architecture")
		assert.EqualValues(t, 500, status.Code)
	})

	t.Run("When the mapping is not found it should not attempt finalization", func(t *testing.T) {
		handler, store := newCRUDServiceHandler(t, handlerTestEvaluator{})
		store.EXPECT().Delete(gomock.Any(), firstOrg, "missing").Return(false, nil)
		status := handler.DeleteLabelSyncMapping(ctx, firstOrg, "missing")
		assert.EqualValues(t, 200, status.Code)
	})

	t.Run("When finalization fails it should return an internal server error", func(t *testing.T) {
		handler, store := newCRUDServiceHandler(t, handlerTestEvaluator{})
		store.EXPECT().Delete(gomock.Any(), firstOrg, "finalize-failure").Return(true, nil)
		store.EXPECT().FinalizeDelete(gomock.Any(), firstOrg, "finalize-failure").Return(false, errors.New("finalization failed"))
		status := handler.DeleteLabelSyncMapping(ctx, firstOrg, "finalize-failure")
		assert.EqualValues(t, 500, status.Code)
	})
}
