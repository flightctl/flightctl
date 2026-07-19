package enrollmentconfig

import (
	"context"
	"net/http"
	"testing"

	cacfg "github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/certificatesigningrequest"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

const (
	statusSuccessCode  = int32(200)
	statusNotFoundCode = int32(404)
)

type fakeCSRService struct {
	certificatesigningrequest.Service
	items map[string]*domain.CertificateSigningRequest
}

func newFakeCSRService() *fakeCSRService {
	return &fakeCSRService{items: map[string]*domain.CertificateSigningRequest{}}
}

func (f *fakeCSRService) GetCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, name string) (*domain.CertificateSigningRequest, domain.Status) {
	csr, ok := f.items[name]
	if !ok {
		return nil, domain.Status{Code: http.StatusNotFound, Message: "not found"}
	}
	return csr, domain.StatusOK()
}

func newTestCA(t *testing.T) *crypto.CAClient {
	cfg := cacfg.NewDefault(t.TempDir())
	caClient, _, err := crypto.EnsureCA(cfg)
	require.NoError(t, err)
	return caClient
}

func TestGetEnrollmentConfig(t *testing.T) {
	t.Run("When no csr param is given it should return the CA bundle without a client certificate", func(t *testing.T) {
		h := NewServiceHandler(newFakeCSRService(), newTestCA(t), "agent.example.com", "https://ui.example.com")

		result, status := h.GetEnrollmentConfig(context.Background(), uuid.New(), domain.GetEnrollmentConfigParams{})
		require.Equal(t, statusSuccessCode, status.Code)
		require.NotNil(t, result)
		require.Equal(t, "agent.example.com", result.EnrollmentService.Service.Server)
		require.Equal(t, "https://ui.example.com", result.EnrollmentService.EnrollmentUiEndpoint)
		require.Empty(t, result.EnrollmentService.Authentication.ClientCertificateData)
	})

	t.Run("When csr param references an issued certificate it should include it", func(t *testing.T) {
		csrSvc := newFakeCSRService()
		cert := []byte("fake-cert-bytes")
		csrSvc.items["csr1"] = &domain.CertificateSigningRequest{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("csr1")},
			Status:   &domain.CertificateSigningRequestStatus{Certificate: &cert},
		}
		h := NewServiceHandler(csrSvc, newTestCA(t), "agent.example.com", "")

		result, status := h.GetEnrollmentConfig(context.Background(), uuid.New(), domain.GetEnrollmentConfigParams{Csr: lo.ToPtr("csr1")})
		require.Equal(t, statusSuccessCode, status.Code)
		require.NotNil(t, result)
		require.NotEmpty(t, result.EnrollmentService.Authentication.ClientCertificateData)
	})

	t.Run("When csr param references a missing CSR it should return not found", func(t *testing.T) {
		h := NewServiceHandler(newFakeCSRService(), newTestCA(t), "", "")

		_, status := h.GetEnrollmentConfig(context.Background(), uuid.New(), domain.GetEnrollmentConfigParams{Csr: lo.ToPtr("missing")})
		require.Equal(t, statusNotFoundCode, status.Code)
	})
}
