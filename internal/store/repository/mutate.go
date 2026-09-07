package repository

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

type RepositoryMutation struct {
	Repository *domain.Repository
}

func (m *RepositoryMutation) Resource() *domain.Repository { return m.Repository }

func (m *RepositoryMutation) SetResource(repository *domain.Repository) { m.Repository = repository }

func (m *RepositoryMutation) Clone() (store.ResourceMutation[domain.Repository], error) {
	out := &RepositoryMutation{}
	if m.Repository != nil {
		cloned, err := store.CloneJSON(m.Repository)
		if err != nil {
			return nil, err
		}
		out.Repository = cloned
	}
	return out, nil
}

func (m *RepositoryMutation) RequireExisting() error {
	if m.Repository == nil {
		return flterrors.ErrResourceNotFound
	}
	return nil
}

type RepositoryApplyFunc func(m *RepositoryMutation) error

var _ store.ResourceMutation[domain.Repository] = (*RepositoryMutation)(nil)

func (s *RepositoryStore) Mutate(ctx context.Context, orgId uuid.UUID, name string, previous *domain.Repository, apply RepositoryApplyFunc) (*domain.Repository, *domain.Repository, bool, error) {
	if previous != nil && lo.FromPtr(previous.Metadata.Name) != name {
		previous = nil
	}
	return s.genericStore.Mutate(ctx, orgId, name, previous, store.MutateHooks[domain.Repository]{
		Wrap: func(repository *domain.Repository) store.ResourceMutation[domain.Repository] {
			return &RepositoryMutation{Repository: repository}
		},
		PersistCreate: func(ctx context.Context, orgId uuid.UUID, m store.ResourceMutation[domain.Repository]) (*domain.Repository, error) {
			return s.Create(ctx, orgId, m.Resource())
		},
		PersistUpdate: func(ctx context.Context, orgId uuid.UUID, _ string, before *domain.Repository, m store.ResourceMutation[domain.Repository]) (bool, error) {
			return s.Update(ctx, orgId, before, m.Resource())
		},
	}, func(m store.ResourceMutation[domain.Repository]) error {
		return apply(m.(*RepositoryMutation))
	})
}

func (s *RepositoryStore) Create(ctx context.Context, orgId uuid.UUID, repository *domain.Repository) (*domain.Repository, error) {
	if repository == nil {
		return nil, flterrors.ErrResourceIsNil
	}
	repositoryModel, err := model.NewRepositoryFromApiResource(repository)
	if err != nil {
		return nil, err
	}
	repositoryModel.OrgID = orgId
	repositoryModel.Generation = lo.ToPtr(int64(1))
	repositoryModel.ResourceVersion = lo.ToPtr(int64(1))

	result := s.getDB(ctx).Create(repositoryModel)
	if result.Error != nil {
		return nil, store.ErrorFromGormError(result.Error)
	}
	return repositoryModel.ToApiResource()
}

func (s *RepositoryStore) Update(ctx context.Context, orgId uuid.UUID, before, repository *domain.Repository) (bool, error) {
	existing, err := model.NewRepositoryFromApiResource(before)
	if err != nil {
		return false, err
	}
	existing.OrgID = orgId

	fromAPI, err := model.NewRepositoryFromApiResource(repository)
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

	repository.Metadata.Generation = lo.ToPtr(generation)
	repository.Metadata.ResourceVersion = lo.ToPtr(strconv.FormatInt(lo.FromPtr(existing.ResourceVersion)+1, 10))
	return false, nil
}
