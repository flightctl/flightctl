package resourcesync

import (
	"context"
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

type ResourceSyncMutation struct {
	ResourceSync *domain.ResourceSync
}

func (m *ResourceSyncMutation) Resource() *domain.ResourceSync { return m.ResourceSync }

func (m *ResourceSyncMutation) SetResource(resourceSync *domain.ResourceSync) {
	m.ResourceSync = resourceSync
}

func (m *ResourceSyncMutation) Clone() (store.ResourceMutation[domain.ResourceSync], error) {
	out := &ResourceSyncMutation{}
	if m.ResourceSync != nil {
		cloned, err := store.CloneJSON(m.ResourceSync)
		if err != nil {
			return nil, err
		}
		out.ResourceSync = cloned
	}
	return out, nil
}

func (m *ResourceSyncMutation) RequireExisting() error {
	if m.ResourceSync == nil {
		return flterrors.ErrResourceNotFound
	}
	return nil
}

type ResourceSyncApplyFunc func(m *ResourceSyncMutation) error

var _ store.ResourceMutation[domain.ResourceSync] = (*ResourceSyncMutation)(nil)

func (s *ResourceSyncStore) Mutate(ctx context.Context, orgId uuid.UUID, name string, previous *domain.ResourceSync, apply ResourceSyncApplyFunc) (*domain.ResourceSync, *domain.ResourceSync, bool, error) {
	if previous != nil && lo.FromPtr(previous.Metadata.Name) != name {
		previous = nil
	}
	return s.genericStore.Mutate(ctx, orgId, name, previous, store.MutateHooks[domain.ResourceSync]{
		Wrap: func(resourceSync *domain.ResourceSync) store.ResourceMutation[domain.ResourceSync] {
			return &ResourceSyncMutation{ResourceSync: resourceSync}
		},
		PersistCreate: func(ctx context.Context, orgId uuid.UUID, m store.ResourceMutation[domain.ResourceSync]) (*domain.ResourceSync, error) {
			return s.Create(ctx, orgId, m.Resource())
		},
		PersistUpdate: func(ctx context.Context, orgId uuid.UUID, _ string, before *domain.ResourceSync, m store.ResourceMutation[domain.ResourceSync]) (bool, error) {
			return s.Update(ctx, orgId, before, m.Resource())
		},
	}, func(m store.ResourceMutation[domain.ResourceSync]) error {
		return apply(m.(*ResourceSyncMutation))
	})
}

func (s *ResourceSyncStore) Create(ctx context.Context, orgId uuid.UUID, resourceSync *domain.ResourceSync) (*domain.ResourceSync, error) {
	if resourceSync == nil {
		return nil, flterrors.ErrResourceIsNil
	}
	resourceSyncModel, err := model.NewResourceSyncFromApiResource(resourceSync)
	if err != nil {
		return nil, err
	}
	resourceSyncModel.OrgID = orgId
	resourceSyncModel.Generation = lo.ToPtr(int64(1))
	resourceSyncModel.ResourceVersion = lo.ToPtr(int64(1))

	result := s.getDB(ctx).Create(resourceSyncModel)
	if result.Error != nil {
		return nil, store.ErrorFromGormError(result.Error)
	}
	return resourceSyncModel.ToApiResource()
}

func (s *ResourceSyncStore) Update(ctx context.Context, orgId uuid.UUID, before, resourceSync *domain.ResourceSync) (bool, error) {
	existing, err := model.NewResourceSyncFromApiResource(before)
	if err != nil {
		return false, err
	}
	existing.OrgID = orgId

	fromAPI, err := model.NewResourceSyncFromApiResource(resourceSync)
	if err != nil {
		return false, err
	}
	fromAPI.OrgID = orgId

	generation := lo.FromPtr(existing.Generation)
	if !fromAPI.HasSameSpecAs(existing) {
		generation++
	}

	updates := map[string]interface{}{
		"spec":             fromAPI.Spec,
		"labels":           model.MakeJSONMap(fromAPI.Labels),
		"annotations":      model.MakeJSONMap(fromAPI.Annotations),
		"owner":            fromAPI.Owner,
		"generation":       generation,
		"status":           fromAPI.Status,
		"resource_version": gorm.Expr("resource_version + 1"),
	}

	result := s.getDB(ctx).Model(existing).Where("resource_version = ?", lo.FromPtr(existing.ResourceVersion)).Updates(updates)
	if result.Error != nil {
		err := store.ErrorFromGormError(result.Error)
		return strings.Contains(err.Error(), "deadlock"), err
	}
	if result.RowsAffected == 0 {
		return true, flterrors.ErrNoRowsUpdated
	}

	resourceSync.Metadata.Generation = lo.ToPtr(generation)
	resourceSync.Metadata.ResourceVersion = lo.ToPtr(strconv.FormatInt(lo.FromPtr(existing.ResourceVersion)+1, 10))
	return false, nil
}
