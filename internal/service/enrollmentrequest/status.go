package enrollmentrequest

import (
	"context"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/google/uuid"
)

func (h *ServiceHandler) updateStatus(ctx context.Context, orgId uuid.UUID, resource *domain.EnrollmentRequest) (*domain.EnrollmentRequest, *domain.EnrollmentRequest, error) {
	if resource == nil || resource.Metadata.Name == nil || resource.Status == nil {
		return nil, nil, flterrors.ErrResourceIsNil
	}
	updated, previous, _, err := h.store.Mutate(ctx, orgId, *resource.Metadata.Name, nil, func(mutation *store.Mutation[domain.EnrollmentRequest]) error {
		if err := mutation.RequireExisting(); err != nil {
			return err
		}
		current := mutation.Resource()
		if resource.Metadata.CreationTimestamp != nil && (current.Metadata.CreationTimestamp == nil || !resource.Metadata.CreationTimestamp.Equal(*current.Metadata.CreationTimestamp)) {
			return flterrors.ErrResourceVersionConflict
		}
		status := *resource.Status
		status.EnrollmentCertificateName = current.Status.EnrollmentCertificateName
		current.Status = &status
		return nil
	})
	return updated, previous, store.ErrorFromGormError(err)
}
