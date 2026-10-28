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
	"time"

	cacfg "github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/consts"
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
	namedCSR *domain.CertificateSigningRequest
	items    []domain.CertificateSigningRequest
	calls    int
}

func (f *fakeCSRService) GetCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, name string) (*domain.CertificateSigningRequest, domain.Status) {
	return f.namedCSR, domain.StatusOK()
}

func (f *fakeCSRService) ListCertificateSigningRequests(ctx context.Context, orgId uuid.UUID, params domain.ListCertificateSigningRequestsParams, opts ...selector.FieldSelectorOption) (*domain.CertificateSigningRequestList, domain.Status) {
	f.calls++
	return &domain.CertificateSigningRequestList{Items: f.items}, domain.StatusOK()
}

func TestNewEnrollmentAuthMiddlewareWhenRequiredDependencyIsMissingItShouldReturnError(t *testing.T) {
	tempDir := t.TempDir()
	caClient, _, err := crypto.EnsureCA(cacfg.NewDefault(tempDir))
	require.NoError(t, err)
	statusCache, err := NewRedisEnrollmentCSRStatusCache(&fakeEnrollmentStatusKVStore{}, time.Minute)
	require.NoError(t, err)

	middleware, err := NewEnrollmentAuthMiddleware(nil, &fakeCSRService{}, statusCache, logrus.New())
	require.Nil(t, middleware)
	require.ErrorContains(t, err, "ca is required")

	middleware, err = NewEnrollmentAuthMiddleware(caClient, nil, statusCache, logrus.New())
	require.Nil(t, middleware)
	require.ErrorContains(t, err, "csrStatuses is required")

	middleware, err = NewEnrollmentAuthMiddleware(caClient, &fakeCSRService{}, nil, logrus.New())
	require.Nil(t, middleware)
	require.ErrorContains(t, err, "statusCache is required")

	middleware, err = NewEnrollmentAuthMiddleware(caClient, &fakeCSRService{}, statusCache, nil)
	require.Nil(t, middleware)
	require.ErrorContains(t, err, "log is required")
}

func TestEnrollmentAuthMiddlewareStartAndStopWhenContextIsCanceledItShouldStopOnlyOnce(t *testing.T) {
	tempDir := t.TempDir()
	caClient, _, err := crypto.EnsureCA(cacfg.NewDefault(tempDir))
	require.NoError(t, err)
	statusCache, err := NewRedisEnrollmentCSRStatusCache(&fakeEnrollmentStatusKVStore{}, time.Minute)
	require.NoError(t, err)
	middleware, err := NewEnrollmentAuthMiddleware(caClient, &fakeCSRService{}, statusCache, logrus.New())
	require.NoError(t, err)

	middleware.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, middleware.Start(ctx))
	require.Error(t, middleware.Start(ctx))

	cancel()
	middleware.Stop()
	middleware.Stop()
}

func TestAuthenticateEnrollment(t *testing.T) {
	tests := []struct {
		name                string
		legacyCertificate   bool
		hasItem             bool
		itemCondition       domain.ConditionType
		cachedCondition     domain.ConditionType
		revoked             bool
		requests            int
		wantCode            int
		wantCSRRequests     int
		wantStatusCacheGets int
		wantStatusCacheSets int
		wantCSRName         string
	}{
		{
			name:                "When CSR is denied it should reject",
			hasItem:             true,
			itemCondition:       domain.ConditionTypeCertificateSigningRequestDenied,
			requests:            1,
			wantCode:            http.StatusUnauthorized,
			wantCSRRequests:     1,
			wantStatusCacheGets: 1,
			wantStatusCacheSets: 1,
		},
		{
			name:                "When denied CSR status is cached it should reject without reading CSR service",
			cachedCondition:     domain.ConditionTypeCertificateSigningRequestDenied,
			requests:            2,
			wantCode:            http.StatusUnauthorized,
			wantStatusCacheGets: 2,
		},
		{
			name:                "When no CSR matches it should cache missing status",
			requests:            2,
			wantCode:            http.StatusUnauthorized,
			wantCSRRequests:     1,
			wantStatusCacheGets: 2,
			wantStatusCacheSets: 1,
		},
		{
			name:                "When legacy credential CSR is denied it should reject",
			legacyCertificate:   true,
			hasItem:             true,
			itemCondition:       domain.ConditionTypeCertificateSigningRequestDenied,
			requests:            1,
			wantCode:            http.StatusUnauthorized,
			wantCSRRequests:     1,
			wantStatusCacheGets: 1,
			wantStatusCacheSets: 1,
		},
		{
			name:                "When CSR is revoked it should reject",
			hasItem:             true,
			itemCondition:       domain.ConditionTypeCertificateSigningRequestApproved,
			revoked:             true,
			requests:            1,
			wantCode:            http.StatusUnauthorized,
			wantCSRRequests:     1,
			wantStatusCacheGets: 1,
			wantStatusCacheSets: 1,
		},
		{
			name:                "When revoked CSR status is cached it should reject without reading CSR service",
			cachedCondition:     domain.ConditionTypeCertificateSigningRequestApproved,
			revoked:             true,
			requests:            2,
			wantCode:            http.StatusUnauthorized,
			wantStatusCacheGets: 2,
		},
		{
			name:                "When CSR is approved it should allow, cache status, and carry the CSR name",
			hasItem:             true,
			itemCondition:       domain.ConditionTypeCertificateSigningRequestApproved,
			requests:            2,
			wantCode:            http.StatusOK,
			wantCSRRequests:     1,
			wantStatusCacheGets: 2,
			wantStatusCacheSets: 1,
			wantCSRName:         "enrollment-credential",
		},
		{
			name:                "When approved CSR status is cached it should authenticate with the cached CSR name",
			cachedCondition:     domain.ConditionTypeCertificateSigningRequestApproved,
			requests:            2,
			wantCode:            http.StatusOK,
			wantStatusCacheGets: 2,
			wantCSRName:         "enrollment-credential",
		},
		{
			name:                "When a legacy approved credential is resolved it should carry the resolved CSR name",
			legacyCertificate:   true,
			hasItem:             true,
			itemCondition:       domain.ConditionTypeCertificateSigningRequestApproved,
			requests:            1,
			wantCode:            http.StatusOK,
			wantCSRRequests:     1,
			wantStatusCacheGets: 1,
			wantStatusCacheSets: 1,
			wantCSRName:         "enrollment-credential",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			tempDir := t.TempDir()
			caClient, _, err := crypto.EnsureCA(cacfg.NewDefault(tempDir))
			require.NoError(t, err)

			csrName := "enrollment-credential"
			peerCertificate := namedEnrollmentCertificate(t, ctx, caClient, tempDir, csrName)
			if test.legacyCertificate {
				peerCertificate = legacyEnrollmentCertificate(t, ctx, caClient)
			}
			certificatePEM, err := fcrypto.EncodeCertificatePEM(peerCertificate)
			require.NoError(t, err)

			csrs := &fakeCSRService{}
			if test.hasItem {
				item := enrollmentCSR(csrName, test.itemCondition, &certificatePEM)
				if test.revoked {
					item.Status.Revocation = testRevocation()
				}
				csrs.items = []domain.CertificateSigningRequest{item}
			}

			statusCache, kv := newTestEnrollmentCSRStatusCache()
			if test.cachedCondition != "" {
				status := domain.CertificateSigningRequestStatus{
					Conditions: []domain.Condition{{
						Type:   test.cachedCondition,
						Status: domain.ConditionStatusTrue,
					}},
				}
				if test.revoked {
					status.Revocation = testRevocation()
				}
				entry := enrollmentCSRStatusCacheEntry{Name: &csrName, Status: &status}
				require.NoError(t, statusCache.Set(ctx, org.DefaultID, crypto.CertificateDigest(peerCertificate.Raw), entry))
				kv.sets = 0
			}

			middleware, err := NewEnrollmentAuthMiddleware(caClient, csrs, statusCache, logrus.New())
			require.NoError(t, err)
			handler := middleware.AuthenticateEnrollment(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				identity, ok := r.Context().Value(consts.IdentityCtxKey).(*EnrollmentIdentity)
				require.True(t, ok)
				if test.wantCSRName == "" {
					require.Nil(t, identity.GetEnrollmentCSRName())
				} else {
					require.NotNil(t, identity.GetEnrollmentCSRName())
					require.Equal(t, test.wantCSRName, *identity.GetEnrollmentCSRName())
				}
				w.WriteHeader(http.StatusOK)
			}))

			for range test.requests {
				request := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(contextWithCert(peerCertificate))
				request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{peerCertificate}}
				recorder := httptest.NewRecorder()

				handler.ServeHTTP(recorder, request)

				require.Equal(t, test.wantCode, recorder.Code)
			}

			require.Equal(t, test.wantCSRRequests, csrs.calls)
			require.Equal(t, test.wantStatusCacheGets, kv.gets)
			require.Equal(t, test.wantStatusCacheSets, kv.sets)
		})
	}
}

func namedEnrollmentCertificate(t *testing.T, ctx context.Context, caClient *crypto.CAClient, tempDir, name string) *x509.Certificate {
	t.Helper()

	clientCert, err := caClient.MakeClientCertificate(ctx, filepath.Join(tempDir, "client.crt"), filepath.Join(tempDir, "client.key"), name, 1)
	require.NoError(t, err)
	return clientCert.Certs[0]
}

func legacyEnrollmentCertificate(t *testing.T, ctx context.Context, caClient *crypto.CAClient) *x509.Certificate {
	t.Helper()

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
	return peerCertificate
}

func testRevocation() *domain.EnrollmentCertificateRevocation {
	return &domain.EnrollmentCertificateRevocation{
		Reason:    "Compromised",
		RevokedBy: "security-officer",
		RevokedAt: time.Now().UTC(),
	}
}

func enrollmentCSR(name string, condition domain.ConditionType, certificatePEM *[]byte) domain.CertificateSigningRequest {
	return domain.CertificateSigningRequest{
		Metadata: domain.ObjectMeta{Name: &name},
		Status: &domain.CertificateSigningRequestStatus{
			Certificate: certificatePEM,
			Conditions: []domain.Condition{{
				Type:   condition,
				Status: domain.ConditionStatusTrue,
			}},
		},
	}
}
