package tpmcsr

import (
	"context"
	"fmt"
	"net/http"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/enrollmentrequest"
	"github.com/flightctl/flightctl/internal/tpm"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
)

// Verifier confirms a TPM-based CertificateSigningRequest against the owning
// EnrollmentRequest's attested TPM material.
type Verifier struct {
	enrollments enrollmentrequest.Service
}

// NewVerifier returns a Verifier that looks up enrollment requests via the
// enrollmentrequest service.
func NewVerifier(enrollments enrollmentrequest.Service) *Verifier {
	return &Verifier{enrollments: enrollments}
}

// VerifyTPMCSRRequest validates the TPM chain of trust for csr. On verification
// failure it sets the TPMVerified condition to false and returns nil (matching
// the historical CSR-handler behavior). It returns a non-nil error only when
// the CSR request bytes cannot be parsed as a TCG CSR.
func (v *Verifier) VerifyTPMCSRRequest(ctx context.Context, orgId uuid.UUID, csr *domain.CertificateSigningRequest) error {
	if csr.Status == nil {
		csr.Status = &domain.CertificateSigningRequestStatus{}
	}
	csrBytes, isTPM := tpm.ParseTCGCSRBytes(string(csr.Spec.Request))
	if !isTPM {
		return fmt.Errorf("parsing TCG CSR")
	}

	// setTPMVerifiedFalse takes an already-formatted message rather than a format string + args
	// so that `go vet`'s printf check does not flag call sites passing a non-constant message
	// (e.g. notTPMBasedMessage below) as a "non-constant format string" error.
	setTPMVerifiedFalse := func(message string) {
		domain.SetStatusCondition(&csr.Status.Conditions, domain.Condition{
			Message: message,
			Reason:  domain.TPMVerificationFailedReason,
			Status:  domain.ConditionStatusFalse,
			Type:    domain.ConditionTypeCertificateSigningRequestTPMVerified,
		})
	}

	kind, owner, err := util.GetResourceOwner(csr.Metadata.Owner)
	if err != nil {
		setTPMVerifiedFalse("Failed to determine resource owner")
		return nil
	}
	if kind != domain.DeviceKind {
		setTPMVerifiedFalse(fmt.Sprintf("The CSR's owner is not a %s", domain.DeviceKind))
		return nil
	}

	// TODO this should be retrieved from the device rather than from the ER
	er, status := v.enrollments.GetEnrollmentRequest(ctx, orgId, owner)
	if status.Code != http.StatusOK {
		setTPMVerifiedFalse(fmt.Sprintf("Unable to find CSR's owner: %s/%s", orgId, owner))
		return nil
	}

	notTPMBasedMessage := fmt.Sprintf("The CSR's owner %s is not TPM based.", lo.FromPtr(csr.Metadata.Owner))
	if er.Status == nil || !domain.IsStatusConditionTrue(er.Status.Conditions, domain.ConditionTypeEnrollmentRequestTPMVerified) {
		setTPMVerifiedFalse(notTPMBasedMessage)
		return nil
	}

	erBytes, isTPM := tpm.ParseTCGCSRBytes(er.Spec.Csr)
	if !isTPM {
		setTPMVerifiedFalse(notTPMBasedMessage)
		return nil
	}

	parsed, err := tpm.ParseTCGCSR(erBytes)
	if err != nil {
		setTPMVerifiedFalse(notTPMBasedMessage)
		return nil
	}

	if err = tpm.VerifyTCGCSRSigningChain(csrBytes, parsed.CSRContents.Payload.AttestPub); err != nil {
		setTPMVerifiedFalse(err.Error())
		return nil
	}
	domain.SetStatusCondition(&csr.Status.Conditions, domain.Condition{
		Message: "TPM chain of trust verified",
		Reason:  "TPMVerificationSucceeded",
		Status:  domain.ConditionStatusTrue,
		Type:    domain.ConditionTypeCertificateSigningRequestTPMVerified,
	})

	return nil
}
