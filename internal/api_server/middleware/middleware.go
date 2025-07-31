package middleware

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"

	"github.com/flightctl/flightctl/internal/auth"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/crypto/signer"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/org"
	"github.com/flightctl/flightctl/internal/util"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/flightctl/flightctl/pkg/reqid"
	chi "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
)

// RequestSizeLimiter returns a middleware that limits the URL length and the number of request headers.
func RequestSizeLimiter(maxURLLength int, maxNumHeaders int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if len(r.URL.String()) > maxURLLength {
				http.Error(w, fmt.Sprintf("URL too long, exceeds %d characters", maxURLLength), http.StatusRequestURITooLong)
				return
			}
			if len(r.Header) > maxNumHeaders {
				http.Error(w, fmt.Sprintf("Request has too many headers, exceeds %d", maxNumHeaders), http.StatusRequestHeaderFieldsTooLarge)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get(chi.RequestIDHeader)
		if requestID == "" {
			requestID = reqid.NextRequestID()
		}
		ctx := context.WithValue(r.Context(), chi.RequestIDKey, requestID)
		w.Header().Set(chi.RequestIDHeader, requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func AddEventMetadataToCtx(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		ctx = context.WithValue(ctx, consts.EventSourceComponentCtxKey, "flightctl-api")
		userName := "none"
		if auth.GetConfiguredAuthType() != auth.AuthTypeNil {
			identity, err := common.GetIdentity(ctx)
			if err == nil && identity != nil {
				userName = identity.Username
			}
		}
		ctx = context.WithValue(ctx, consts.EventActorCtxKey, fmt.Sprintf("user:%s", userName))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// OrgIDExtractor extracts an organization ID from an HTTP request.
type OrgIDExtractor func(*http.Request) (uuid.UUID, error)

// QueryOrgIDExtractor is the default extractor that reads the org_id from the query string.
var QueryOrgIDExtractor OrgIDExtractor = extractOrgIDFromRequestQuery

// CertOrgIDExtractor reads the org_id from the client certificate.
var CertOrgIDExtractor OrgIDExtractor = extractOrgIDFromRequestCert

// AddOrgIDToCtx extracts organization ID using the supplied extractor, validates it
// using the provided resolver, and injects it into the request context.
func AddOrgIDToCtx(resolver *org.Resolver, extractor OrgIDExtractor) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			orgID, err := extractor(r)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			// Validate the organization ID
			if err := resolver.Validate(ctx, orgID); err != nil {
				if errors.Is(err, flterrors.ErrResourceNotFound) {
					http.Error(w, fmt.Sprintf("Organization not found: %s", orgID), http.StatusNotFound)
					return
				}
				http.Error(w, fmt.Sprintf("Failed to validate organization: %s", err.Error()), http.StatusInternalServerError)
				return
			}

			// Set org ID in context and proceed
			ctx = util.WithOrganizationID(ctx, orgID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func extractOrgIDFromRequestQuery(r *http.Request) (uuid.UUID, error) {
	orgIDParam := r.URL.Query().Get("org_id")
	if orgIDParam == "" {
		return org.DefaultID, nil
	}

	parsedID, err := org.Parse(orgIDParam)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid org_id parameter: %w", err)
	}
	return parsedID, nil
}

// getOrgIDFromCert tries to extract the OrgID (as a uuid.UUID) from the given
// X.509 certificate. The OrgID is expected to be stored in a custom extension
// identified by OIDOrgID. If the extension is not found or cannot be parsed as
// a UUID, an error is returned.
func getOrgIDFromCert(cert *x509.Certificate) (uuid.UUID, error) {
	if cert == nil {
		return uuid.Nil, fmt.Errorf("certificate is nil")
	}

	v, err := fccrypto.GetExtensionValue(cert, signer.OIDOrgID)
	if err != nil {
		return uuid.Nil, err
	}

	orgID, parseErr := org.Parse(v)
	if parseErr != nil {
		return uuid.Nil, fmt.Errorf("invalid org_id extension value: %w", parseErr)
	}
	return orgID, nil
}

// extractOrgIDFromRequestCert extracts organization ID from the client certificate.
// Returns the default organization ID if no certificate is available or if the
// certificate doesn't contain an org ID extension.
func extractOrgIDFromRequestCert(r *http.Request) (uuid.UUID, error) {
	ctx := r.Context()
	peerCertificate, err := signer.PeerCertificateFromCtx(ctx)
	if err != nil {
		return org.DefaultID, nil
	}

	certOrgID, err := getOrgIDFromCert(peerCertificate)
	if err != nil {
		if errors.Is(err, flterrors.ErrExtensionNotFound) {
			return org.DefaultID, nil
		}
		return uuid.Nil, fmt.Errorf("failed to extract organization ID from certificate: %w", err)
	}
	return certOrgID, nil
}
