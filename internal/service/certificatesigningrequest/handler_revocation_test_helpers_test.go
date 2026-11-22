package certificatesigningrequest

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	cacfg "github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/identity"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func revocationIdentityContext() context.Context {
	return context.WithValue(context.Background(), consts.MappedIdentityCtxKey,
		identity.NewMappedIdentity("security-officer", "uid-security-officer", nil, nil, true, nil))
}

func createIssuedEnrollmentCSR(t *testing.T, h *ServiceHandler, ctx context.Context, orgId uuid.UUID, cfg *cacfg.Config, name string) *domain.CertificateSigningRequest {
	t.Helper()
	created, status := h.CreateCertificateSigningRequest(ctx, orgId, domain.CertificateSigningRequest{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
		Spec: domain.CertificateSigningRequestSpec{
			SignerName: cfg.DeviceEnrollmentSignerName,
			Request:    csrPEM(t, name),
			Usages:     validUsages(),
		},
	})
	require.Equal(t, statusCreatedCode, status.Code)
	require.NotNil(t, created.Status.Certificate)
	return created
}

func insertIssuedCSR(t *testing.T, store *fakeCertificateSigningRequestStore, name, signer, certificate string) string {
	t.Helper()
	store.items[name] = &domain.CertificateSigningRequest{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
		Spec: domain.CertificateSigningRequestSpec{
			SignerName: signer,
			Request:    csrPEM(t, name),
			Usages:     validUsages(),
		},
		Status: &domain.CertificateSigningRequestStatus{
			Conditions:  []domain.Condition{{Type: domain.ConditionTypeCertificateSigningRequestApproved, Status: domain.ConditionStatusTrue}},
			Certificate: lo.ToPtr([]byte(certificate)),
		},
	}
	return name
}

func insertLegacySignerCSR(t *testing.T, h *ServiceHandler, store *fakeCertificateSigningRequestStore, ctx context.Context, name string) *domain.CertificateSigningRequest {
	t.Helper()
	tempDir := t.TempDir()
	clientCert, err := h.ca.MakeClientCertificate(ctx, filepath.Join(tempDir, "client.crt"), filepath.Join(tempDir, "client.key"), name, 1)
	require.NoError(t, err)
	certificatePEM, err := fcrypto.EncodeCertificatePEM(clientCert.Certs[0])
	require.NoError(t, err)
	issuedAt := time.Now().UTC()
	csr := &domain.CertificateSigningRequest{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr(name), CreationTimestamp: lo.ToPtr(time.Now().UTC())},
		Spec: domain.CertificateSigningRequestSpec{
			SignerName: "legacy.example/changed",
			Request:    csrPEM(t, name),
			Usages:     validUsages(),
		},
		Status: &domain.CertificateSigningRequestStatus{
			Certificate: &certificatePEM,
			Conditions: []domain.Condition{{
				Type:   domain.ConditionTypeCertificateSigningRequestApproved,
				Status: domain.ConditionStatusTrue,
				Reason: "Approved",
			}},
			IssuedAt: &issuedAt,
		},
	}
	store.items[name] = csr
	return csr
}

func filterRevocationEvents(events []*domain.Event) []*domain.Event {
	var filtered []*domain.Event
	for _, event := range events {
		if event != nil && event.Reason == domain.EventReasonEnrollmentCredentialRevoked {
			filtered = append(filtered, event)
		}
	}
	return filtered
}

func assertRevocationPersisted(t *testing.T, h *ServiceHandler, ctx context.Context, orgId uuid.UUID, name string) *domain.CertificateSigningRequest {
	t.Helper()
	persisted, status := h.GetCertificateSigningRequest(ctx, orgId, name)
	require.Equal(t, statusSuccessCode, status.Code)
	return persisted
}
