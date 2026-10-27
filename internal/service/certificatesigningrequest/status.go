package certificatesigningrequest

import (
	"context"
	"reflect"

	"github.com/flightctl/flightctl/internal/crypto/signer"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	certificatesigningrequeststore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/google/uuid"
)

// isEnrollmentIssuanceEligible reports whether a CSR is signed by the configured
// enrollment signer. Issuance metadata is strictly gated on that configuration.
func (h *ServiceHandler) isEnrollmentIssuanceEligible(resource *domain.CertificateSigningRequest) bool {
	name := h.ca.Cfg.DeviceEnrollmentSignerName
	if name == "" || resource == nil {
		return false
	}
	return resource.Spec.SignerName == name
}

// isProtectedEnrollmentCredential reports whether the retained certificate is an
// enrollment credential that must stay immutable. Legacy credentials whose
// configured signer changed are recognized through the retained certificate's
// signer extension.
func (h *ServiceHandler) isProtectedEnrollmentCredential(resource *domain.CertificateSigningRequest) bool {
	name := h.ca.Cfg.DeviceEnrollmentSignerName
	if name == "" || resource == nil {
		return false
	}
	if resource.Spec.SignerName == name {
		return true
	}
	if resource.Status == nil || resource.Status.Certificate == nil {
		return false
	}
	leaf, err := fcrypto.ParseCertificatePEM(*resource.Status.Certificate)
	if err != nil {
		return false
	}
	retainedSigner, err := signer.GetSignerNameExtension(leaf)
	return err == nil && retainedSigner == name
}

func (h *ServiceHandler) updateStatus(ctx context.Context, orgId uuid.UUID, candidate *domain.CertificateSigningRequest, previous *domain.CertificateSigningRequest) (*domain.CertificateSigningRequest, error) {
	if candidate == nil || candidate.Metadata.Name == nil || candidate.Status == nil {
		return nil, flterrors.ErrResourceIsNil
	}
	updated, _, _, err := h.store.Mutate(ctx, orgId, *candidate.Metadata.Name, previous, func(mutation *certificatesigningrequeststore.CertificateSigningRequestMutation) error {
		if err := mutation.RequireExisting(); err != nil {
			return err
		}
		current := mutation.Resource()
		enrollment := h.isProtectedEnrollmentCredential(current) || h.isProtectedEnrollmentCredential(candidate)
		issuing := candidate.Status.Certificate != nil && len(*candidate.Status.Certificate) > 0
		if enrollment {
			if issuing && (!reflect.DeepEqual(candidate.Spec, current.Spec) || candidate.Metadata.CreationTimestamp == nil) {
				return flterrors.ErrResourceVersionConflict
			}
			if candidate.Metadata.CreationTimestamp != nil && (current.Metadata.CreationTimestamp == nil || !candidate.Metadata.CreationTimestamp.Equal(*current.Metadata.CreationTimestamp)) {
				return flterrors.ErrResourceVersionConflict
			}
		}
		status := *candidate.Status
		if enrollment && current.Status != nil && current.Status.Certificate != nil && len(*current.Status.Certificate) > 0 {
			status.Certificate = current.Status.Certificate
			status.IssuedAt = current.Status.IssuedAt
		}
		if !enrollment || status.Certificate == nil || len(*status.Certificate) == 0 {
			status.IssuedAt = nil
		}
		mutation.CertificateSigningRequest.Status = &status
		return nil
	})
	return updated, err
}
