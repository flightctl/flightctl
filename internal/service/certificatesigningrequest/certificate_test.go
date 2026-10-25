package certificatesigningrequest

import (
	"context"
	"path/filepath"
	"testing"

	cacfg "github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store/selector"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type fakeCertificateService struct {
	Service
	items  []domain.CertificateSigningRequest
	status domain.Status
}

func (f *fakeCertificateService) ListCertificateSigningRequests(ctx context.Context, orgId uuid.UUID, params domain.ListCertificateSigningRequestsParams, opts ...selector.FieldSelectorOption) (*domain.CertificateSigningRequestList, domain.Status) {
	return &domain.CertificateSigningRequestList{Items: f.items}, f.status
}

func TestCertificateForLeafWhenResolvingExactCertificateItShouldReturnTheMatchingCSR(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	caClient, _, err := crypto.EnsureCA(cacfg.NewDefault(tempDir))
	require.NoError(t, err)

	csrName := "enrollment-credential"
	clientCert, err := caClient.MakeClientCertificate(ctx, filepath.Join(tempDir, "client.crt"), filepath.Join(tempDir, "client.key"), csrName, 1)
	require.NoError(t, err)
	leaf := clientCert.Certs[0]
	certificatePEM, err := fcrypto.EncodeCertificatePEM(leaf)
	require.NoError(t, err)
	matchingCSR := domain.CertificateSigningRequest{
		Metadata: domain.ObjectMeta{Name: &csrName},
		Status: &domain.CertificateSigningRequestStatus{
			Certificate: &certificatePEM,
			Conditions: []domain.Condition{{
				Type:   domain.ConditionTypeCertificateSigningRequestApproved,
				Status: domain.ConditionStatusTrue,
			}},
		},
	}

	tests := []struct {
		name       string
		items      []domain.CertificateSigningRequest
		status     domain.Status
		wantCSR    *domain.CertificateSigningRequest
		wantErrStr string
	}{
		{
			name:    "When the certificate matches it should return the CSR",
			items:   []domain.CertificateSigningRequest{matchingCSR},
			status:  domain.StatusOK(),
			wantCSR: &matchingCSR,
		},
		{
			name:   "When no certificate matches it should return no CSR",
			items:  nil,
			status: domain.StatusOK(),
		},
		{
			name:       "When multiple certificates match it should return an ambiguity error",
			items:      []domain.CertificateSigningRequest{matchingCSR, matchingCSR},
			status:     domain.StatusOK(),
			wantErrStr: ErrAmbiguousCertificate.Error(),
		},
		{
			name:       "When listing fails it should return the lookup error",
			status:     domain.StatusInternalServerError("lookup failed"),
			wantErrStr: "lookup failed",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeCertificateService{items: test.items, status: test.status}

			csr, err := CertificateForLeaf(ctx, service, uuid.New(), leaf)

			if test.wantErrStr != "" {
				require.ErrorContains(t, err, test.wantErrStr)
				require.Nil(t, csr)
				return
			}
			require.NoError(t, err)
			if test.wantCSR == nil {
				require.Nil(t, csr)
				return
			}
			require.Equal(t, test.wantCSR, csr)
		})
	}
}
