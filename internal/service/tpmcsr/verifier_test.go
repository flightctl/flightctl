package tpmcsr

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/enrollmentrequest"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

type fakeEnrollmentService struct {
	enrollmentrequest.Service
	ers map[string]*domain.EnrollmentRequest
}

func (f *fakeEnrollmentService) GetEnrollmentRequest(_ context.Context, _ uuid.UUID, name string) (*domain.EnrollmentRequest, domain.Status) {
	er, ok := f.ers[name]
	if !ok {
		return nil, domain.StatusResourceNotFound(domain.EnrollmentRequestKind, name)
	}
	return er, domain.StatusOK()
}

// tcgCSRHeaderBytes returns the minimal byte sequence internal/tpm.IsTCGCSRFormat recognizes
// as TCG-CSR-IDEVID version 1.0 (the leading 4-byte big-endian version marker 0x01000100),
// padded to the parser's 12-byte minimum length.
func tcgCSRHeaderBytes() []byte {
	return []byte{0x01, 0x00, 0x01, 0x00, 0, 0, 0, 0, 0, 0, 0, 0}
}

func TestNewVerifier(t *testing.T) {
	ers := &fakeEnrollmentService{ers: map[string]*domain.EnrollmentRequest{}}
	v := NewVerifier(ers)
	require.Same(t, ers, v.enrollments)
}

func TestVerifyTPMCSRRequest(t *testing.T) {
	t.Run("When the request is not a TCG CSR it should return a parse error", func(t *testing.T) {
		v := NewVerifier(&fakeEnrollmentService{ers: map[string]*domain.EnrollmentRequest{}})
		csr := &domain.CertificateSigningRequest{
			Spec: domain.CertificateSigningRequestSpec{Request: []byte("not-a-tpm-csr")},
		}

		err := v.VerifyTPMCSRRequest(context.Background(), uuid.New(), csr)
		require.Error(t, err)
		require.Contains(t, err.Error(), "parsing TCG CSR")
	})

	t.Run("When the owner is not a device it should mark TPM verification false", func(t *testing.T) {
		v := NewVerifier(&fakeEnrollmentService{ers: map[string]*domain.EnrollmentRequest{}})
		csr := &domain.CertificateSigningRequest{
			Metadata: domain.ObjectMeta{Owner: lo.ToPtr("Fleet/myfleet")},
			Spec:     domain.CertificateSigningRequestSpec{Request: tcgCSRHeaderBytes()},
			Status:   &domain.CertificateSigningRequestStatus{},
		}

		err := v.VerifyTPMCSRRequest(context.Background(), uuid.New(), csr)
		require.NoError(t, err)
		cond := domain.FindStatusCondition(csr.Status.Conditions, domain.ConditionTypeCertificateSigningRequestTPMVerified)
		require.NotNil(t, cond)
		require.Equal(t, domain.ConditionStatusFalse, cond.Status)
	})

	t.Run("When the owning enrollment request cannot be found it should mark TPM verification false", func(t *testing.T) {
		v := NewVerifier(&fakeEnrollmentService{ers: map[string]*domain.EnrollmentRequest{}})
		csr := &domain.CertificateSigningRequest{
			Metadata: domain.ObjectMeta{Owner: lo.ToPtr("Device/missing-device")},
			Spec:     domain.CertificateSigningRequestSpec{Request: tcgCSRHeaderBytes()},
			Status:   &domain.CertificateSigningRequestStatus{},
		}

		err := v.VerifyTPMCSRRequest(context.Background(), uuid.New(), csr)
		require.NoError(t, err)
		cond := domain.FindStatusCondition(csr.Status.Conditions, domain.ConditionTypeCertificateSigningRequestTPMVerified)
		require.NotNil(t, cond)
		require.Equal(t, domain.ConditionStatusFalse, cond.Status)
	})

	t.Run("When the owning enrollment request was not TPM verified it should mark TPM verification false", func(t *testing.T) {
		ers := &fakeEnrollmentService{ers: map[string]*domain.EnrollmentRequest{
			"my-device": {
				Metadata: domain.ObjectMeta{Name: lo.ToPtr("my-device")},
				Status:   &domain.EnrollmentRequestStatus{Conditions: []domain.Condition{}},
			},
		}}
		v := NewVerifier(ers)
		csr := &domain.CertificateSigningRequest{
			Metadata: domain.ObjectMeta{Owner: lo.ToPtr("Device/my-device")},
			Spec:     domain.CertificateSigningRequestSpec{Request: tcgCSRHeaderBytes()},
			Status:   &domain.CertificateSigningRequestStatus{},
		}

		err := v.VerifyTPMCSRRequest(context.Background(), uuid.New(), csr)
		require.NoError(t, err)
		cond := domain.FindStatusCondition(csr.Status.Conditions, domain.ConditionTypeCertificateSigningRequestTPMVerified)
		require.NotNil(t, cond)
		require.Equal(t, domain.ConditionStatusFalse, cond.Status)
	})
}
