package enrollmentrequest

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	certificatesigningrequeststore "github.com/flightctl/flightctl/internal/store/certificatesigningrequest"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

// fakeCSRStore is a small in-memory implementation of
// internal/store/certificatesigningrequest.Store, scoped to what GetEnrollmentConfig calls
// (Get only - every other accessor is unused by this package and panics via the embedded nil
// interface if reached).
type fakeCSRStore struct {
	certificatesigningrequeststore.Store
	items map[string]*domain.CertificateSigningRequest
}

func newFakeCSRStore() *fakeCSRStore {
	return &fakeCSRStore{items: map[string]*domain.CertificateSigningRequest{}}
}

func (f *fakeCSRStore) InitialMigration(ctx context.Context) error { return nil }

func (f *fakeCSRStore) Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.CertificateSigningRequest, error) {
	csr, ok := f.items[name]
	if !ok {
		return nil, flterrors.ErrResourceNotFound
	}
	return csr, nil
}

func TestGetEnrollmentConfig(t *testing.T) {
	t.Run("When no csr param is given it should return the CA bundle without a client certificate", func(t *testing.T) {
		csrStore := newFakeCSRStore()
		caClient := newTestCA(t)
		ev := &fakeEventsService{}
		deviceSvc, _ := newTestDeviceService(ev)
		h := NewServiceHandler(newFakeEnrollmentRequestStore(), deviceSvc, csrStore, caClient, &fakeKVStore{}, ev, logrus.New(), nil, "agent.example.com", "https://ui.example.com")

		result, status := h.GetEnrollmentConfig(context.Background(), uuid.New(), domain.GetEnrollmentConfigParams{})
		require.Equal(t, statusSuccessCode, status.Code)
		require.NotNil(t, result)
		require.Equal(t, "agent.example.com", result.EnrollmentService.Service.Server)
		require.Equal(t, "https://ui.example.com", result.EnrollmentService.EnrollmentUiEndpoint)
		require.Empty(t, result.EnrollmentService.Authentication.ClientCertificateData)
	})

	t.Run("When csr param references an issued certificate it should include it", func(t *testing.T) {
		csrStore := newFakeCSRStore()
		cert := []byte("fake-cert-bytes")
		csrStore.items["csr1"] = &domain.CertificateSigningRequest{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("csr1")},
			Status:   &domain.CertificateSigningRequestStatus{Certificate: &cert},
		}
		caClient := newTestCA(t)
		ev := &fakeEventsService{}
		deviceSvc, _ := newTestDeviceService(ev)
		h := NewServiceHandler(newFakeEnrollmentRequestStore(), deviceSvc, csrStore, caClient, &fakeKVStore{}, ev, logrus.New(), nil, "agent.example.com", "")

		result, status := h.GetEnrollmentConfig(context.Background(), uuid.New(), domain.GetEnrollmentConfigParams{Csr: lo.ToPtr("csr1")})
		require.Equal(t, statusSuccessCode, status.Code)
		require.NotNil(t, result)
		require.NotEmpty(t, result.EnrollmentService.Authentication.ClientCertificateData)
	})

	t.Run("When csr param references a missing CSR it should return not found", func(t *testing.T) {
		csrStore := newFakeCSRStore()
		caClient := newTestCA(t)
		ev := &fakeEventsService{}
		deviceSvc, _ := newTestDeviceService(ev)
		h := NewServiceHandler(newFakeEnrollmentRequestStore(), deviceSvc, csrStore, caClient, &fakeKVStore{}, ev, logrus.New(), nil, "", "")

		_, status := h.GetEnrollmentConfig(context.Background(), uuid.New(), domain.GetEnrollmentConfigParams{Csr: lo.ToPtr("missing")})
		require.Equal(t, statusNotFoundCode, status.Code)
	})
}
