package labelsyncmapping

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeStore struct {
	mappings            map[uuid.UUID]map[string]*domain.LabelSyncMapping
	deleteErr           error
	finalizeDeleteErr   error
	finalizeDeleteCalls int
}

type rejectingExpressionValidator struct{ err error }

func (v rejectingExpressionValidator) ValidateLabelSyncMapping(context.Context, domain.LabelSyncMapping) error {
	return v.err
}

func newFakeStore() *fakeStore {
	return &fakeStore{mappings: make(map[uuid.UUID]map[string]*domain.LabelSyncMapping)}
}

func (*fakeStore) InitialMigration(context.Context) error { return nil }

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

func (*fakeStore) Revision(context.Context, uuid.UUID, domain.LabelSyncMappingResourceType) (int64, error) {
	return 0, nil
}

func (s *fakeStore) FinalizeDelete(context.Context, uuid.UUID, string) (bool, error) {
	s.finalizeDeleteCalls++
	return false, s.finalizeDeleteErr
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

func TestLabelSyncMappingLifecycle(t *testing.T) {
	ctx := context.Background()
	firstOrg := uuid.New()
	secondOrg := uuid.New()
	store := newFakeStore()
	handler := NewServiceHandler(store)

	t.Run("When the expression validator rejects a mapping it should return 422 without persisting", func(t *testing.T) {
		validationStore := newFakeStore()
		validationHandler := NewServiceHandler(validationStore, rejectingExpressionValidator{err: errors.New("expression output does not match map mode")})
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

	t.Run("When a mapping is deleted it should remain readable during cleanup", func(t *testing.T) {
		status := handler.DeleteLabelSyncMapping(ctx, firstOrg, "architecture")
		require.EqualValues(t, 200, status.Code)
		assert.Equal(t, 1, store.finalizeDeleteCalls)
		deleted, status := handler.GetLabelSyncMapping(ctx, firstOrg, "architecture")
		require.EqualValues(t, 200, status.Code)
		assert.NotNil(t, deleted.Metadata.DeletionTimestamp)
		assert.Equal(t, "Pending", lo.FromPtr(deleted.Status.Conditions)[0].Reason)
	})

	t.Run("When deletion fails it should not attempt finalization", func(t *testing.T) {
		deleteStore := newFakeStore()
		deleteStore.deleteErr = errors.New("delete failed")
		deleteHandler := NewServiceHandler(deleteStore)

		status := deleteHandler.DeleteLabelSyncMapping(ctx, firstOrg, "architecture")
		assert.EqualValues(t, 500, status.Code)
		assert.Zero(t, deleteStore.finalizeDeleteCalls)
	})

	t.Run("When the mapping is not found it should not attempt finalization", func(t *testing.T) {
		missingStore := newFakeStore()
		missingHandler := NewServiceHandler(missingStore)

		status := missingHandler.DeleteLabelSyncMapping(ctx, firstOrg, "missing")
		assert.EqualValues(t, 200, status.Code)
		assert.Zero(t, missingStore.finalizeDeleteCalls)
	})

	t.Run("When finalization fails it should return an internal server error", func(t *testing.T) {
		finalizeStore := newFakeStore()
		finalizeHandler := NewServiceHandler(finalizeStore)
		_, createStatus := finalizeHandler.CreateLabelSyncMapping(ctx, firstOrg, mapping("finalize-failure"))
		require.EqualValues(t, 201, createStatus.Code)
		finalizeStore.finalizeDeleteErr = errors.New("finalization failed")

		status := finalizeHandler.DeleteLabelSyncMapping(ctx, firstOrg, "finalize-failure")
		assert.EqualValues(t, 500, status.Code)
		assert.Equal(t, 1, finalizeStore.finalizeDeleteCalls)
	})
}
