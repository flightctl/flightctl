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
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/org"
	orgmodel "github.com/flightctl/flightctl/internal/org/model"
	"github.com/google/uuid"
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
// Tests for the query extractor.
// -----------------------------------------------------------------------------
func TestExtractOrgIDFromRequestQuery(t *testing.T) {
	validID := uuid.New()

	cases := []struct {
		name     string
		rawQuery string
		wantID   uuid.UUID
		wantErr  bool
	}{
		{"no param returns default", "", org.DefaultID, false},
		{"empty param returns default", "org_id=", org.DefaultID, false},
		{"valid id", fmt.Sprintf("org_id=%s", validID), validID, false},
		{"invalid uuid", "org_id=not-a-uuid", uuid.Nil, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/?"+tc.rawQuery, nil)
			got, err := extractOrgIDFromRequestQuery(context.Background(), r)
			if tc.wantErr {
				assert.Error(t, err)
				assert.Equal(t, uuid.Nil, got)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.wantID, got)
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
			mappedIdentity := identity.NewMappedIdentity("test-user", "test-uid", []*orgmodel.Organization{org}, []string{}, identity.NewIssuer("test", "test-issuer"))
			ctx = context.WithValue(ctx, consts.MappedIdentityCtxKey, mappedIdentity)
		}
		return ctx
	}

	cases := []struct {
		name    string
		ctx     context.Context
		wantID  uuid.UUID
		wantErr bool
	}{
		{"no certificate", context.Background(), uuid.Nil, true},
		{"cert without extension", contextWithCert(&x509.Certificate{}), uuid.Nil, true},
		{"cert with valid extension", contextWithCertAndOrg(createCertWithOrgID(validID), orgEntity), validID, false},
		{"cert with invalid uuid", contextWithCert(func() *x509.Certificate {
			encoded, _ := asn1.Marshal("not-a-uuid")
			return &x509.Certificate{ExtraExtensions: []pkix.Extension{{Id: signer.OIDOrgID, Value: encoded}}}
		}()), uuid.Nil, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(tc.ctx)
			got, err := extractOrgIDFromRequestCert(tc.ctx, r)
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tc.wantID, got)
		})
	}
}
