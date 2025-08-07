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
	"time"

	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/crypto/signer"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/org"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

// mockOrgLookup mocks OrgLookup's GetByID.
type mockOrgLookup struct {
	t       *testing.T
	wantID  uuid.UUID
	respOrg *model.Organization
	respErr error
	called  bool
}

func (m *mockOrgLookup) GetByID(_ context.Context, id uuid.UUID) (*model.Organization, error) {
	if m.t != nil && m.wantID != uuid.Nil && m.wantID != id {
		m.t.Errorf("unexpected id: got %v, want %v", id, m.wantID)
	}
	m.called = true
	return m.respOrg, m.respErr
}

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
			got, err := extractOrgIDFromRequestQuery(r)
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

	cases := []struct {
		name    string
		ctx     context.Context
		wantID  uuid.UUID
		wantErr bool
	}{
		{"no certificate", context.Background(), org.DefaultID, false},
		{"cert without extension", contextWithCert(&x509.Certificate{}), org.DefaultID, false},
		{"cert with valid extension", contextWithCert(createCertWithOrgID(validID)), validID, false},
		{"cert with invalid uuid", contextWithCert(func() *x509.Certificate {
			encoded, _ := asn1.Marshal("not-a-uuid")
			return &x509.Certificate{ExtraExtensions: []pkix.Extension{{Id: signer.OIDOrgID, Value: encoded}}}
		}()), uuid.Nil, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(tc.ctx)
			got, err := extractOrgIDFromRequestCert(r)
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tc.wantID, got)
		})
	}
}

// -----------------------------------------------------------------------------
// Tests for AddOrgIDToCtx middleware. We focus on integration of the extractor
// and the resolver plus context injection.
// -----------------------------------------------------------------------------
func TestAddOrgIDToCtx(t *testing.T) {
	validID := uuid.New()

	type storeResponse struct {
		org *model.Organization
		err error
	}

	cases := []struct {
		name             string
		extractor        func(*http.Request) (uuid.UUID, error)
		storeResp        storeResponse
		wantCode         int
		wantCtxID        uuid.UUID
		wantBodyContains string
		expectNextCalled bool
	}{
		{
			name:             "happy path",
			extractor:        func(r *http.Request) (uuid.UUID, error) { return validID, nil },
			storeResp:        storeResponse{org: &model.Organization{ID: validID}},
			wantCode:         http.StatusTeapot,
			wantCtxID:        validID,
			expectNextCalled: true,
		},
		{
			name:             "extractor error returns 400",
			extractor:        func(r *http.Request) (uuid.UUID, error) { return uuid.Nil, fmt.Errorf("bad param") },
			wantCode:         http.StatusBadRequest,
			wantBodyContains: "bad param",
		},
		{
			name:      "org not found returns 404",
			extractor: func(r *http.Request) (uuid.UUID, error) { return validID, nil },
			storeResp: storeResponse{err: flterrors.ErrResourceNotFound},
			wantCode:  http.StatusNotFound,
		},
		{
			name:             "resolver internal error returns 500",
			extractor:        func(r *http.Request) (uuid.UUID, error) { return validID, nil },
			storeResp:        storeResponse{err: fmt.Errorf("DB down")},
			wantCode:         http.StatusInternalServerError,
			wantBodyContains: "Failed to validate organization",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			storeMock := &mockOrgLookup{t: t}
			if tc.storeResp.org != nil || tc.storeResp.err != nil {
				storeMock.respOrg = tc.storeResp.org
				storeMock.respErr = tc.storeResp.err
				storeMock.wantID = validID
			}

			resolver := org.NewResolver(storeMock, 5*time.Minute)
			mw := AddOrgIDToCtx(resolver, tc.extractor)

			var (
				gotCtxID   uuid.UUID
				nextCalled bool
			)

			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				nextCalled = true
				gotCtxID, _ = util.GetOrgIdFromContext(r.Context())
				w.WriteHeader(http.StatusTeapot)
			})

			rr := httptest.NewRecorder()
			mw(next).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

			assert.Equal(t, tc.wantCode, rr.Code)

			if tc.expectNextCalled {
				assert.True(t, nextCalled, "next handler should be called")
				assert.Equal(t, tc.wantCtxID, gotCtxID)
			} else {
				assert.False(t, nextCalled, "next handler should not be called")
			}

			if tc.wantBodyContains != "" {
				assert.Contains(t, rr.Body.String(), tc.wantBodyContains)
			}

			if tc.storeResp.org != nil || tc.storeResp.err != nil {
				assert.True(t, storeMock.called, "store mock should be called")
			} else {
				assert.False(t, storeMock.called, "store mock should not be called")
			}
		})
	}
}
