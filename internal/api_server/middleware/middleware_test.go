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
				assert.Equal(t, tc.wantMiddlewareCode, rr.Code)
				assert.Contains(t, rr.Body.String(), tc.wantMiddlewareErr.Error(),
					"expected response body to contain %q", tc.wantMiddlewareErr)
				return
			}

			assert.Equal(t, http.StatusOK, rr.Code)
			assert.Equal(t, tc.wantContextID, capturedOrgID,
				"expected context org ID %s but got %s", tc.wantContextID, capturedOrgID)
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
