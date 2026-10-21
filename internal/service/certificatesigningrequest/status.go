package certificatesigningrequest

import (
	"context"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/google/uuid"
)

func (h *ServiceHandler) updateStatus(ctx context.Context, orgId uuid.UUID, candidate *domain.CertificateSigningRequest) (*domain.CertificateSigningRequest, error) {
	if candidate == nil || candidate.Metadata.Name == nil || candidate.Status == nil {
		return nil, flterrors.ErrResourceIsNil
	}
	updated, _, _, err := h.store.Mutate(ctx, orgId, *candidate.Metadata.Name, nil, func(mutation *store.Mutation[domain.CertificateSigningRequest]) error {
		if err := mutation.RequireExisting(); err != nil {
			return err
		}
		status := *candidate.Status
		mutation.Resource().Status = &status
		return nil
	})
	return updated, err
}
