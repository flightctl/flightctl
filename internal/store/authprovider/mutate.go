package authprovider

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

type AuthProviderMutation struct {
	AuthProvider *domain.AuthProvider
}

func (m *AuthProviderMutation) Resource() *domain.AuthProvider { return m.AuthProvider }

func (m *AuthProviderMutation) SetResource(authProvider *domain.AuthProvider) {
	m.AuthProvider = authProvider
}

func (m *AuthProviderMutation) Clone() (store.ResourceMutation[domain.AuthProvider], error) {
	out := &AuthProviderMutation{}
	if m.AuthProvider != nil {
		cloned, err := store.CloneJSON(m.AuthProvider)
		if err != nil {
			return nil, err
		}
		out.AuthProvider = cloned
	}
	return out, nil
}

func (m *AuthProviderMutation) RequireExisting() error {
	if m.AuthProvider == nil {
		return flterrors.ErrResourceNotFound
	}
	return nil
}

type AuthProviderApplyFunc func(m *AuthProviderMutation) error

var _ store.ResourceMutation[domain.AuthProvider] = (*AuthProviderMutation)(nil)

func (s *AuthProviderStore) Mutate(ctx context.Context, orgId uuid.UUID, name string, previous *domain.AuthProvider, apply AuthProviderApplyFunc) (*domain.AuthProvider, *domain.AuthProvider, bool, error) {
	if previous != nil && lo.FromPtr(previous.Metadata.Name) != name {
		previous = nil
	}
	return s.genericStore.Mutate(ctx, orgId, name, previous, store.MutateHooks[domain.AuthProvider]{
		Wrap: func(authProvider *domain.AuthProvider) store.ResourceMutation[domain.AuthProvider] {
			return &AuthProviderMutation{AuthProvider: authProvider}
		},
		PersistCreate: func(ctx context.Context, orgId uuid.UUID, m store.ResourceMutation[domain.AuthProvider]) (*domain.AuthProvider, error) {
			return s.Create(ctx, orgId, m.Resource())
		},
		PersistUpdate: func(ctx context.Context, orgId uuid.UUID, _ string, before *domain.AuthProvider, m store.ResourceMutation[domain.AuthProvider]) (bool, error) {
			return s.Update(ctx, orgId, before, m.Resource())
		},
	}, func(m store.ResourceMutation[domain.AuthProvider]) error {
		return apply(m.(*AuthProviderMutation))
	})
}

func (s *AuthProviderStore) Create(ctx context.Context, orgId uuid.UUID, authProvider *domain.AuthProvider) (*domain.AuthProvider, error) {
	if authProvider == nil {
		return nil, flterrors.ErrResourceIsNil
	}
	authProviderModel, err := model.NewAuthProviderFromApiResource(authProvider)
	if err != nil {
		return nil, err
	}
	authProviderModel.OrgID = orgId
	authProviderModel.Generation = lo.ToPtr(int64(1))
	authProviderModel.ResourceVersion = lo.ToPtr(int64(1))

	result := s.getDB(ctx).Create(authProviderModel)
	if result.Error != nil {
		return nil, store.ErrorFromGormError(result.Error)
	}
	return authProviderModel.ToApiResource()
}

func (s *AuthProviderStore) Update(ctx context.Context, orgId uuid.UUID, before, authProvider *domain.AuthProvider) (bool, error) {
	existing, err := model.NewAuthProviderFromApiResource(before)
	if err != nil {
		return false, err
	}
	existing.OrgID = orgId

	fromAPI, err := model.NewAuthProviderFromApiResource(authProvider)
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

	authProvider.Metadata.Generation = lo.ToPtr(generation)
	authProvider.Metadata.ResourceVersion = lo.ToPtr(strconv.FormatInt(lo.FromPtr(existing.ResourceVersion)+1, 10))
	return false, nil
}
