package middleware

import (
	"context"
	stdcrypto "crypto"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	cacfg "github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/crypto"
	fcsigner "github.com/flightctl/flightctl/internal/crypto/signer"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/org"
	csrservice "github.com/flightctl/flightctl/internal/service/certificatesigningrequest"
	"github.com/flightctl/flightctl/internal/store/selector"
	fcrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

type fakeCSRService struct {
	csrservice.Service
	csr   *domain.CertificateSigningRequest
	items []domain.CertificateSigningRequest
	calls int
}

func (f *fakeCSRService) GetCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, name string) (*domain.CertificateSigningRequest, domain.Status) {
	f.calls++
	return f.csr, domain.StatusOK()
}

func (f *fakeCSRService) ListCertificateSigningRequests(ctx context.Context, orgId uuid.UUID, params domain.ListCertificateSigningRequestsParams, opts ...selector.FieldSelectorOption) (*domain.CertificateSigningRequestList, domain.Status) {
	f.calls++
	return &domain.CertificateSigningRequestList{Items: f.items}, domain.StatusOK()
}

type fakeEnrollmentCSRStatusCache struct {
	statuses map[string]domain.CertificateSigningRequestStatus
	gets     int
	sets     int
}

func (f *fakeEnrollmentCSRStatusCache) Get(ctx context.Context, orgId uuid.UUID, certificateDigest string) (domain.CertificateSigningRequestStatus, bool, error) {
	f.gets++
	status, found := f.statuses[orgId.String()+"/"+certificateDigest]
	return status, found, nil
}

func (f *fakeEnrollmentCSRStatusCache) Set(ctx context.Context, orgId uuid.UUID, certificateDigest string, status domain.CertificateSigningRequestStatus) error {
	f.sets++
	f.statuses[orgId.String()+"/"+certificateDigest] = status
	return nil
}

func TestAuthenticateEnrollmentWhenCSRIsDeniedItShouldReject(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	caClient, _, err := crypto.EnsureCA(cacfg.NewDefault(tempDir))
	require.NoError(t, err)

	csrName := "denied-enrollment-credential"
	clientCert, err := caClient.MakeClientCertificate(ctx, filepath.Join(tempDir, "client.crt"), filepath.Join(tempDir, "client.key"), csrName, 1)
	require.NoError(t, err)

	csrs := &fakeCSRService{
		csr: &domain.CertificateSigningRequest{
			Metadata: domain.ObjectMeta{Name: &csrName},
			Status: &domain.CertificateSigningRequestStatus{
				Conditions: []domain.Condition{{
					Type:   domain.ConditionTypeCertificateSigningRequestDenied,
					Status: domain.ConditionStatusTrue,
				}},
			},
		},
	}
	statusCache := &fakeEnrollmentCSRStatusCache{statuses: map[string]domain.CertificateSigningRequestStatus{}}
	middleware := NewEnrollmentAuthMiddleware(caClient, csrs, statusCache, logrus.New())
	handler := middleware.AuthenticateEnrollment(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	peerCertificate := clientCert.Certs[0]
	request := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(contextWithCert(peerCertificate))
	request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{peerCertificate}}
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusUnauthorized, recorder.Code)
	require.Equal(t, 1, csrs.calls)
	require.Equal(t, 1, statusCache.sets)
}

func TestAuthenticateEnrollmentWhenDeniedCSRStatusIsCachedItShouldRejectWithoutReadingCSRService(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	caClient, _, err := crypto.EnsureCA(cacfg.NewDefault(tempDir))
	require.NoError(t, err)

	csrName := "cached-denied-enrollment-credential"
	clientCert, err := caClient.MakeClientCertificate(ctx, filepath.Join(tempDir, "client.crt"), filepath.Join(tempDir, "client.key"), csrName, 1)
	require.NoError(t, err)
	peerCertificate := clientCert.Certs[0]
	certificateDigest := crypto.CertificateDigest(peerCertificate.Raw)

	csrs := &fakeCSRService{}
	denied := domain.CertificateSigningRequestStatus{
		Conditions: []domain.Condition{{
			Type:   domain.ConditionTypeCertificateSigningRequestDenied,
			Status: domain.ConditionStatusTrue,
		}},
	}
	statusCache := &fakeEnrollmentCSRStatusCache{
		statuses: map[string]domain.CertificateSigningRequestStatus{
			org.DefaultID.String() + "/" + certificateDigest: denied,
		},
	}
	middleware := NewEnrollmentAuthMiddleware(caClient, csrs, statusCache, logrus.New())
	handler := middleware.AuthenticateEnrollment(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for range 2 {
		request := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(contextWithCert(peerCertificate))
		request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{peerCertificate}}
		recorder := httptest.NewRecorder()

		handler.ServeHTTP(recorder, request)

		require.Equal(t, http.StatusUnauthorized, recorder.Code)
	}

	require.Equal(t, 2, statusCache.gets)
	require.Equal(t, 0, csrs.calls)
}

func TestAuthenticateEnrollmentWhenLegacyCredentialCSRIsDeniedItShouldReject(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	caClient, _, err := crypto.EnsureCA(cacfg.NewDefault(tempDir))
	require.NoError(t, err)

	_, privateKey, err := fcrypto.NewKeyPair()
	require.NoError(t, err)
	signerKey, ok := privateKey.(stdcrypto.Signer)
	require.True(t, ok)
	rawCSR, err := fcrypto.MakeCSR(signerKey, "legacy-enrollment-credential")
	require.NoError(t, err)
	parsedCSR, err := fcrypto.ParseCSR(rawCSR)
	require.NoError(t, err)
	peerCertificate, err := caClient.IssueRequestedClientCertificate(ctx, parsedCSR, 3600,
		fcsigner.WithExtension(fcsigner.OIDSignerName, caClient.Cfg.DeviceEnrollmentSignerName),
		fcsigner.WithExtension(fcsigner.OIDOrgID, org.DefaultID.String()),
	)
	require.NoError(t, err)
	certificatePEM, err := fcrypto.EncodeCertificatePEM(peerCertificate)
	require.NoError(t, err)

	csrName := "legacy-denied-enrollment-credential"
	csrs := &fakeCSRService{
		items: []domain.CertificateSigningRequest{{
			Metadata: domain.ObjectMeta{Name: &csrName},
			Status: &domain.CertificateSigningRequestStatus{
				Certificate: &certificatePEM,
				Conditions: []domain.Condition{{
					Type:   domain.ConditionTypeCertificateSigningRequestDenied,
					Status: domain.ConditionStatusTrue,
				}},
			},
		}},
	}
	statusCache := &fakeEnrollmentCSRStatusCache{statuses: map[string]domain.CertificateSigningRequestStatus{}}
	middleware := NewEnrollmentAuthMiddleware(caClient, csrs, statusCache, logrus.New())
	handler := middleware.AuthenticateEnrollment(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	request := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(contextWithCert(peerCertificate))
	request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{peerCertificate}}
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusUnauthorized, recorder.Code)
	require.Equal(t, 1, csrs.calls)
}

func TestAuthenticateEnrollmentWhenCSRIsApprovedItShouldAllowAndCacheStatus(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	caClient, _, err := crypto.EnsureCA(cacfg.NewDefault(tempDir))
	require.NoError(t, err)

	csrName := "approved-enrollment-credential"
	clientCert, err := caClient.MakeClientCertificate(ctx, filepath.Join(tempDir, "client.crt"), filepath.Join(tempDir, "client.key"), csrName, 1)
	require.NoError(t, err)

	csrs := &fakeCSRService{
		csr: &domain.CertificateSigningRequest{
			Metadata: domain.ObjectMeta{Name: &csrName},
			Status: &domain.CertificateSigningRequestStatus{
				Conditions: []domain.Condition{{
					Type:   domain.ConditionTypeCertificateSigningRequestApproved,
					Status: domain.ConditionStatusTrue,
				}},
			},
		},
	}
	statusCache := &fakeEnrollmentCSRStatusCache{statuses: map[string]domain.CertificateSigningRequestStatus{}}
	middleware := NewEnrollmentAuthMiddleware(caClient, csrs, statusCache, logrus.New())
	handler := middleware.AuthenticateEnrollment(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	peerCertificate := clientCert.Certs[0]
	for range 2 {
		request := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(contextWithCert(peerCertificate))
		request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{peerCertificate}}
		recorder := httptest.NewRecorder()

		handler.ServeHTTP(recorder, request)

		require.Equal(t, http.StatusOK, recorder.Code)
	}

	require.Equal(t, 1, csrs.calls)
	require.Equal(t, 2, statusCache.gets)
	require.Equal(t, 1, statusCache.sets)
}
