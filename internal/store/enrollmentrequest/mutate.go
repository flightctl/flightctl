package enrollmentrequest

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

type EnrollmentRequestMutation struct {
	EnrollmentRequest *domain.EnrollmentRequest
}

func (m *EnrollmentRequestMutation) Resource() *domain.EnrollmentRequest {
	return m.EnrollmentRequest
}

func (m *EnrollmentRequestMutation) SetResource(er *domain.EnrollmentRequest) {
	m.EnrollmentRequest = er
}

func (m *EnrollmentRequestMutation) Clone() (store.ResourceMutation[domain.EnrollmentRequest], error) {
	out := &EnrollmentRequestMutation{}
	if m.EnrollmentRequest != nil {
		cloned, err := store.CloneJSON(m.EnrollmentRequest)
		if err != nil {
			return nil, err
		}
		out.EnrollmentRequest = cloned
	}
	return out, nil
}

func (m *EnrollmentRequestMutation) RequireExisting() error {
	if m.EnrollmentRequest == nil {
		return flterrors.ErrResourceNotFound
	}
	return nil
}

type EnrollmentRequestApplyFunc func(m *EnrollmentRequestMutation) error

var _ store.ResourceMutation[domain.EnrollmentRequest] = (*EnrollmentRequestMutation)(nil)

func (s *EnrollmentRequestStore) Mutate(ctx context.Context, orgId uuid.UUID, name string, previous *domain.EnrollmentRequest, apply EnrollmentRequestApplyFunc) (*domain.EnrollmentRequest, *domain.EnrollmentRequest, bool, error) {
	if previous != nil && lo.FromPtr(previous.Metadata.Name) != name {
		previous = nil
	}
	return s.genericStore.Mutate(ctx, orgId, name, previous, store.MutateHooks[domain.EnrollmentRequest]{
		Wrap: func(er *domain.EnrollmentRequest) store.ResourceMutation[domain.EnrollmentRequest] {
			return &EnrollmentRequestMutation{EnrollmentRequest: er}
		},
		PersistCreate: func(ctx context.Context, orgId uuid.UUID, m store.ResourceMutation[domain.EnrollmentRequest]) (*domain.EnrollmentRequest, error) {
			return s.Create(ctx, orgId, m.Resource())
		},
		PersistUpdate: func(ctx context.Context, orgId uuid.UUID, _ string, before *domain.EnrollmentRequest, m store.ResourceMutation[domain.EnrollmentRequest]) (bool, error) {
			return s.Update(ctx, orgId, before, m.Resource())
		},
	}, func(m store.ResourceMutation[domain.EnrollmentRequest]) error {
		return apply(m.(*EnrollmentRequestMutation))
	})
}

func (s *EnrollmentRequestStore) Create(ctx context.Context, orgId uuid.UUID, er *domain.EnrollmentRequest) (*domain.EnrollmentRequest, error) {
	if er == nil {
		return nil, flterrors.ErrResourceIsNil
	}
	erModel, err := model.NewEnrollmentRequestFromApiResource(er)
	if err != nil {
		return nil, err
	}
	erModel.OrgID = orgId
	erModel.Generation = lo.ToPtr(int64(1))
	erModel.ResourceVersion = lo.ToPtr(int64(1))

	result := s.getDB(ctx).Create(erModel)
	if result.Error != nil {
		return nil, store.ErrorFromGormError(result.Error)
	}
	return erModel.ToApiResource()
}

func (s *EnrollmentRequestStore) Update(ctx context.Context, orgId uuid.UUID, before, er *domain.EnrollmentRequest) (bool, error) {
	existing, err := model.NewEnrollmentRequestFromApiResource(before)
	if err != nil {
		return false, err
	}
	existing.OrgID = orgId

	fromAPI, err := model.NewEnrollmentRequestFromApiResource(er)
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

	er.Metadata.Generation = lo.ToPtr(generation)
	er.Metadata.ResourceVersion = lo.ToPtr(strconv.FormatInt(lo.FromPtr(existing.ResourceVersion)+1, 10))
	return false, nil
}
