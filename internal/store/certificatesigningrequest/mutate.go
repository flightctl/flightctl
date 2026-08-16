package certificatesigningrequest

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

type CertificateSigningRequestMutation struct {
	CertificateSigningRequest *domain.CertificateSigningRequest
}

func (m *CertificateSigningRequestMutation) Resource() *domain.CertificateSigningRequest {
	return m.CertificateSigningRequest
}

func (m *CertificateSigningRequestMutation) SetResource(csr *domain.CertificateSigningRequest) {
	m.CertificateSigningRequest = csr
}

func (m *CertificateSigningRequestMutation) Clone() (store.ResourceMutation[domain.CertificateSigningRequest], error) {
	out := &CertificateSigningRequestMutation{}
	if m.CertificateSigningRequest != nil {
		cloned, err := store.CloneJSON(m.CertificateSigningRequest)
		if err != nil {
			return nil, err
		}
		out.CertificateSigningRequest = cloned
	}
	return out, nil
}

func (m *CertificateSigningRequestMutation) RequireExisting() error {
	if m.CertificateSigningRequest == nil {
		return flterrors.ErrResourceNotFound
	}
	return nil
}

type CertificateSigningRequestApplyFunc func(m *CertificateSigningRequestMutation) error

var _ store.ResourceMutation[domain.CertificateSigningRequest] = (*CertificateSigningRequestMutation)(nil)

func (s *CertificateSigningRequestStore) Mutate(ctx context.Context, orgId uuid.UUID, name string, previous *domain.CertificateSigningRequest, apply CertificateSigningRequestApplyFunc) (*domain.CertificateSigningRequest, *domain.CertificateSigningRequest, bool, error) {
	if previous != nil && lo.FromPtr(previous.Metadata.Name) != name {
		previous = nil
	}
	return s.genericStore.Mutate(ctx, orgId, name, previous, store.MutateHooks[domain.CertificateSigningRequest]{
		Wrap: func(csr *domain.CertificateSigningRequest) store.ResourceMutation[domain.CertificateSigningRequest] {
			return &CertificateSigningRequestMutation{CertificateSigningRequest: csr}
		},
		PersistCreate: func(ctx context.Context, orgId uuid.UUID, m store.ResourceMutation[domain.CertificateSigningRequest]) (*domain.CertificateSigningRequest, error) {
			return s.Create(ctx, orgId, m.Resource())
		},
		PersistUpdate: func(ctx context.Context, orgId uuid.UUID, _ string, before *domain.CertificateSigningRequest, m store.ResourceMutation[domain.CertificateSigningRequest]) (bool, error) {
			return s.Update(ctx, orgId, before, m.Resource())
		},
	}, func(m store.ResourceMutation[domain.CertificateSigningRequest]) error {
		return apply(m.(*CertificateSigningRequestMutation))
	})
}

func (s *CertificateSigningRequestStore) Create(ctx context.Context, orgId uuid.UUID, csr *domain.CertificateSigningRequest) (*domain.CertificateSigningRequest, error) {
	if csr == nil {
		return nil, flterrors.ErrResourceIsNil
	}
	csrModel, err := model.NewCertificateSigningRequestFromApiResource(csr)
	if err != nil {
		return nil, err
	}
	csrModel.OrgID = orgId
	csrModel.Generation = lo.ToPtr(int64(1))
	csrModel.ResourceVersion = lo.ToPtr(int64(1))

	result := s.getDB(ctx).Create(csrModel)
	if result.Error != nil {
		return nil, store.ErrorFromGormError(result.Error)
	}
	return csrModel.ToApiResource()
}

func (s *CertificateSigningRequestStore) Update(ctx context.Context, orgId uuid.UUID, before, csr *domain.CertificateSigningRequest) (bool, error) {
	existing, err := model.NewCertificateSigningRequestFromApiResource(before)
	if err != nil {
		return false, err
	}
	existing.OrgID = orgId

	fromAPI, err := model.NewCertificateSigningRequestFromApiResource(csr)
	if err != nil {
		return false, err
	}
	fromAPI.OrgID = orgId

	generation := lo.FromPtr(existing.Generation)
	apiSpecChanged := before != nil && csr != nil &&
		!domain.CertificateSigningRequestSpecsAreEqual(before.Spec, csr.Spec)
	if apiSpecChanged || !fromAPI.HasSameSpecAs(existing) {
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

	csr.Metadata.Generation = lo.ToPtr(generation)
	csr.Metadata.ResourceVersion = lo.ToPtr(strconv.FormatInt(lo.FromPtr(existing.ResourceVersion)+1, 10))
	return false, nil
}
