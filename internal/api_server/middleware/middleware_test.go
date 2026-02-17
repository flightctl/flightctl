package middleware

import (
	"context"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/crypto/signer"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/org"
	orgmodel "github.com/flightctl/flightctl/internal/org/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createCertWithOrgID returns an *x509.Certificate with the organization ID
// stored in an extension identified by signer.OIDOrgID.
func createCertWithOrgID(id uuid.UUID) *x509.Certificate {
	encoded, _ := asn1.Marshal(id.String())
	return &x509.Certificate{
		Subject: pkix.Name{CommonName: "unit-test"},
		ExtraExtensions: []pkix.Extension{{
			Id:       signer.OIDOrgID,
			Critical: false,
			Value:    encoded,
		}},
	}
}

// contextWithCert returns a context containing the supplied certificate under
// the consts.TLSPeerCertificateCtxKey key.
func contextWithCert(cert *x509.Certificate) context.Context {
	return context.WithValue(context.Background(), consts.TLSPeerCertificateCtxKey, cert)
}

// -----------------------------------------------------------------------------
// Tests for SecurityHeaders middleware.
// -----------------------------------------------------------------------------
func TestSecurityHeaders(t *testing.T) {
	handler := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "max-age=31536000; includeSubDomains", rr.Header().Get("Strict-Transport-Security"))
	assert.Equal(t, "nosniff", rr.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "DENY", rr.Header().Get("X-Frame-Options"))
}

// -----------------------------------------------------------------------------
// Tests for ContentSecurityPolicy middleware.
// -----------------------------------------------------------------------------
func TestContentSecurityPolicy(t *testing.T) {
	cases := []struct {
		name   string
		policy string
	}{
		{"strict CSP", StrictCSP},
		{"PAM issuer CSP", PAMIssuerCSP},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := ContentSecurityPolicy(tc.policy)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

			assert.Equal(t, http.StatusOK, rr.Code)
			assert.Equal(t, tc.policy, rr.Header().Get("Content-Security-Policy"))
		})
	}
}

// -----------------------------------------------------------------------------
// Tests for ExtractAndValidateOrg middleware.
// -----------------------------------------------------------------------------
func TestExtractAndValidateOrg(t *testing.T) {
	orgID := uuid.New()
	orgID2 := uuid.New()

	cases := []struct {
		name               string
		rawQuery           string
		userOrgs           []*orgmodel.Organization
		hasMappedIdentity  bool
		wantMiddlewareErr  error
		wantMiddlewareCode int
		wantContextID      uuid.UUID
	}{
		{
			name:               "no param with zero orgs returns no organizations error",
			rawQuery:           "",
			userOrgs:           []*orgmodel.Organization{},
			hasMappedIdentity:  true,
			wantMiddlewareErr:  flterrors.ErrNoOrganizations,
			wantMiddlewareCode: http.StatusForbidden,
		},
		{
			name:              "no param with single org uses that org",
			rawQuery:          "",
			userOrgs:          []*orgmodel.Organization{{ID: orgID, ExternalID: "user-org"}},
			hasMappedIdentity: true,
			wantContextID:     orgID,
		},
		{
			name:     "no param with multiple orgs returns ambiguous error",
			rawQuery: "",
			userOrgs: []*orgmodel.Organization{
				{ID: orgID, ExternalID: "user-org-1"},
				{ID: orgID2, ExternalID: "user-org-2"},
			},
			hasMappedIdentity:  true,
			wantMiddlewareErr:  flterrors.ErrAmbiguousOrganization,
			wantMiddlewareCode: http.StatusBadRequest,
		},
		{
			name:     "explicit org_id with multiple orgs succeeds",
			rawQuery: fmt.Sprintf("org_id=%s", orgID),
			userOrgs: []*orgmodel.Organization{
				{ID: orgID, ExternalID: "user-org-1"},
				{ID: orgID2, ExternalID: "user-org-2"},
			},
			hasMappedIdentity: true,
			wantContextID:     orgID,
		},
		{
			name:               "no mapped identity returns internal server error",
			rawQuery:           "org_id=",
			userOrgs:           nil,
			hasMappedIdentity:  false,
			wantMiddlewareErr:  flterrors.ErrNoMappedIdentity,
			wantMiddlewareCode: http.StatusInternalServerError,
		},
		{
			name:               "explicit org_id with empty user orgs returns forbidden",
			rawQuery:           fmt.Sprintf("org_id=%s", orgID),
			userOrgs:           []*orgmodel.Organization{},
			hasMappedIdentity:  true,
			wantMiddlewareErr:  flterrors.ErrNotOrgMember,
			wantMiddlewareCode: http.StatusForbidden,
		},
		{
			name:               "explicit org_id for org not in user orgs returns forbidden",
			rawQuery:           fmt.Sprintf("org_id=%s", orgID2),
			userOrgs:           []*orgmodel.Organization{{ID: orgID, ExternalID: "user-org"}},
			hasMappedIdentity:  true,
			wantMiddlewareErr:  flterrors.ErrNotOrgMember,
			wantMiddlewareCode: http.StatusForbidden,
		},
		{
			name:               "invalid uuid",
			rawQuery:           "org_id=not-a-uuid",
			userOrgs:           []*orgmodel.Organization{},
			hasMappedIdentity:  true,
			wantMiddlewareErr:  flterrors.ErrInvalidOrgID,
			wantMiddlewareCode: http.StatusBadRequest,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/?"+tc.rawQuery, nil)

			ctx := r.Context()
			if tc.hasMappedIdentity {
				mappedIdentity := identity.NewMappedIdentity(
					"test-user", "test-uid", tc.userOrgs,
					map[string][]string{}, false,
					identity.NewIssuer("test", "test-issuer"),
				)
				ctx = context.WithValue(ctx, consts.MappedIdentityCtxKey, mappedIdentity)
				r = r.WithContext(ctx)
			}

			var capturedOrgID uuid.UUID
			testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capturedOrgID, _ = util.GetOrgIdFromContext(r.Context())
				w.WriteHeader(http.StatusOK)
			})

			logger := logrus.New()
			logger.SetLevel(logrus.DebugLevel)
			middleware := ExtractAndValidateOrg(QueryOrgIDExtractor, logger)

			rr := httptest.NewRecorder()
			middleware(testHandler).ServeHTTP(rr, r)

			if tc.wantMiddlewareErr != nil {
				require.Equal(t, tc.wantMiddlewareCode, rr.Code)
				require.Equal(t, "application/json", rr.Header().Get("Content-Type"))
				expectedReason := reasonForOrgError(tc.wantMiddlewareErr)
				expectedBody := fmt.Sprintf(`{"code":%d,"message":"%s","reason":"%s"}`, tc.wantMiddlewareCode, tc.wantMiddlewareErr.Error(), expectedReason)
				require.JSONEq(t, expectedBody, rr.Body.String())
				return
			}

			require.Equal(t, http.StatusOK, rr.Code)
			require.Equal(t, tc.wantContextID, capturedOrgID,
				"expected context org ID %s but got %s", tc.wantContextID, capturedOrgID)
		})
	}
}

// -----------------------------------------------------------------------------
// Tests for RequestSizeLimiter middleware.
// -----------------------------------------------------------------------------

func TestRequestSizeLimiter(t *testing.T) {
	cases := []struct {
		name           string
		url            string
		numHeaders     int
		maxURLLength   int
		maxNumHeaders  int
		wantCode       int
		wantReason     string
		wantMessage    string
		handlerInvoked bool
	}{
		{
			name:           "valid request",
			url:            "/valid",
			numHeaders:     5,
			maxURLLength:   10,
			maxNumHeaders:  10,
			handlerInvoked: true,
		},
		{
			name:          "url too long",
			url:           "/this-is-too-long",
			numHeaders:    5,
			maxURLLength:  10,
			maxNumHeaders: 10,
			wantCode:      http.StatusRequestURITooLong,
			wantReason:    "RequestURITooLong",
			wantMessage:   "URL too long, exceeds 10 characters",
		},
		{
			name:          "too many headers",
			url:           "/valid",
			numHeaders:    15,
			maxURLLength:  10,
			maxNumHeaders: 10,
			wantCode:      http.StatusRequestHeaderFieldsTooLarge,
			wantReason:    "RequestHeaderFieldsTooLarge",
			wantMessage:   "request has too many headers, exceeds 10",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)

			// Given
			r := httptest.NewRequest(http.MethodGet, tc.url, nil)
			for i := 0; i < tc.numHeaders; i++ {
				r.Header.Set(fmt.Sprintf("X-Test-Header-%d", i), "value")
			}
			w := httptest.NewRecorder()

			handlerInvoked := false
			testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				handlerInvoked = true
				w.WriteHeader(http.StatusOK)
			})

			middleware := RequestSizeLimiter(tc.maxURLLength, tc.maxNumHeaders)

			// When
			middleware(testHandler).ServeHTTP(w, r)

			// Then
			if tc.handlerInvoked {
				require.True(handlerInvoked)
				require.Equal(http.StatusOK, w.Code)
			} else {
				require.False(handlerInvoked)
				require.Equal(tc.wantCode, w.Code)
				require.Equal("application/json", w.Header().Get("Content-Type"))
				expectedBody := fmt.Sprintf(`{"code":%d,"message":"%s","reason":"%s"}`, tc.wantCode, tc.wantMessage, tc.wantReason)
				require.JSONEq(expectedBody, w.Body.String())
			}
		})
	}
}


// -----------------------------------------------------------------------------
// Tests for the certificate extractor.
// -----------------------------------------------------------------------------
func TestExtractOrgIDFromRequestCert(t *testing.T) {
	validID := uuid.New()
	orgEntity := &orgmodel.Organization{
		ID:         validID,
		ExternalID: validID.String(),
	}

	// Helper to create context with cert and mapped identity
	contextWithCertAndOrg := func(cert *x509.Certificate, org *orgmodel.Organization) context.Context {
		ctx := context.WithValue(context.Background(), consts.TLSPeerCertificateCtxKey, cert)
		if org != nil {
			mappedIdentity := identity.NewMappedIdentity("test-user", "test-uid", []*orgmodel.Organization{org}, map[string][]string{}, false, identity.NewIssuer("test", "test-issuer"))
			ctx = context.WithValue(ctx, consts.MappedIdentityCtxKey, mappedIdentity)
		}
		return ctx
	}

	cases := []struct {
		name        string
		ctx         context.Context
		wantID      uuid.UUID
		wantPresent bool
		wantErr     bool
	}{
		{"no certificate", context.Background(), uuid.Nil, false, true},
		{"cert without extension", contextWithCert(&x509.Certificate{}), uuid.Nil, false, true},
		{"cert with valid extension", contextWithCertAndOrg(createCertWithOrgID(validID), orgEntity), validID, true, false},
		{"cert with default org id", contextWithCertAndOrg(createCertWithOrgID(org.DefaultID), orgEntity), org.DefaultID, true, false},
		{"cert with invalid uuid", contextWithCert(func() *x509.Certificate {
			encoded, _ := asn1.Marshal("not-a-uuid")
			return &x509.Certificate{ExtraExtensions: []pkix.Extension{{Id: signer.OIDOrgID, Value: encoded}}}
		}()), uuid.Nil, false, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(tc.ctx)
			got, present, err := extractOrgIDFromRequestCert(tc.ctx, r)
			if tc.wantErr {
				assert.Error(t, err)
				assert.False(t, present)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.wantPresent, present)
			}
			assert.Equal(t, tc.wantID, got)
		})
	}
}
