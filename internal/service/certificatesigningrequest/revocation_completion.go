package certificatesigningrequest

import (
	"context"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store"
	certificatesigningrequeststore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	"github.com/google/uuid"
)

func (h *ServiceHandler) MarkEnrollmentCredentialFlaggingCompleted(ctx context.Context, orgId uuid.UUID, name string) (*domain.CertificateSigningRequest, domain.Status) {
	if name == "" {
		return nil, domain.StatusBadRequest("certificate signing request name is required")
	}
	updated, _, _, err := h.store.Mutate(ctx, orgId, name, nil, func(mutation *certificatesigningrequeststore.CertificateSigningRequestMutation) error {
		if err := mutation.RequireExisting(); err != nil {
			return err
		}
		current := mutation.Resource()
		if current.Status == nil || current.Status.Revocation == nil || current.Status.Revocation.FlaggingCompletedAt != nil {
			return store.ErrMutateSkipWrite
		}
		completedAt := nowFunc().UTC()
		current.Status.Revocation.FlaggingCompletedAt = &completedAt
		return nil
	})
	if err != nil {
		return nil, common.StoreErrorToApiStatus(err, false, domain.CertificateSigningRequestKind, &name)
	}
	return updated, domain.StatusOK()
}
