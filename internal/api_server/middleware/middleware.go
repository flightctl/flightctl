package middleware

import (
	"context"
	"fmt"
	"net/http"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/auth"
	authcommon "github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/contextutil"
	"github.com/flightctl/flightctl/internal/crypto/signer"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/reqid"
	chi "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
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
			identity, ok := contextutil.GetMappedIdentityFromContext(ctx)
			if ok && identity != nil {
				userName = identity.GetUsername()
			}
		}
		ctx = context.WithValue(ctx, consts.EventActorCtxKey, fmt.Sprintf("user:%s", userName))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// OrgIDExtractor extracts an organization ID from an HTTP request.
type OrgIDExtractor func(context.Context, *http.Request) (uuid.UUID, error)

// QueryOrgIDExtractor is the default extractor that reads the org_id from the query string.
var QueryOrgIDExtractor OrgIDExtractor = extractOrgIDFromRequestQuery

// CertOrgIDExtractor reads the org_id from the client certificate.
var CertOrgIDExtractor OrgIDExtractor = extractOrgIDFromRequestCert

// ExtractOrgIDToCtx extracts organization ID using the supplied extractor and sets it in the request context.
// This middleware only extracts and sets the org ID - it does not validate membership.
func ExtractOrgIDToCtx(extractor OrgIDExtractor, logger logrus.FieldLogger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			reqLogger := log.WithReqIDFromCtx(ctx, logger)

			orgID, err := extractor(ctx, r)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			// Log the extracted organization ID
			reqLogger.Debugf("ExtractOrgIDToCtx: extracted orgID=%s from request", orgID.String())

			// If no organization ID was found, use the user's first organization
			if orgID == uuid.Nil {
				mappedIdentity, ok := contextutil.GetMappedIdentityFromContext(ctx)
				if ok && len(mappedIdentity.GetOrganizations()) > 0 {
					orgID = mappedIdentity.GetOrganizations()[0].ID
					reqLogger.Debugf("ExtractOrgIDToCtx: extracted orgID=%s from mapped identity", orgID.String())
				}
			}

			// Set org ID in context and proceed
			ctx = util.WithOrganizationID(ctx, orgID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ValidateOrgMembership validates that the user is a member of the organization in the context.
// This middleware only validates membership - it does not extract the org ID.
func ValidateOrgMembership(logger logrus.FieldLogger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip validation for public auth endpoints
			if authcommon.IsPublicAuthEndpoint(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			ctx := r.Context()
			reqLogger := log.WithReqIDFromCtx(ctx, logger)

			// Get organization ID from context
			orgID, ok := util.GetOrgIdFromContext(ctx)
			if !ok {
				http.Error(w, "No organization ID found in context", http.StatusForbidden)
				return
			}
			// Log the organization ID being validated
			reqLogger.Debugf("ValidateOrgMembership: validating access to orgID=%s", orgID.String())

			// Get mapped identity from context (set by identity mapping middleware)
			mappedIdentity, ok := contextutil.GetMappedIdentityFromContext(ctx)
			if !ok {
				http.Error(w, "No mapped identity found in context", http.StatusInternalServerError)
				return
			}
			// Check if the user is a member of the organization and log organizations
			isMember := false
			userOrgIDs := make([]string, len(mappedIdentity.GetOrganizations()))
			for i, org := range mappedIdentity.GetOrganizations() {
				userOrgIDs[i] = fmt.Sprintf("%s(%s)", org.ExternalID, org.ID.String())
				if org.ID == orgID {
					isMember = true
				}
			}
			reqLogger.Debugf("ValidateOrgMembership: user organizations=%v, isMember=%v", userOrgIDs, isMember)

			if !isMember {
				http.Error(w, fmt.Sprintf("Access denied to organization: %s (user organizations: %v)", orgID, userOrgIDs), http.StatusForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func extractOrgIDFromRequestQuery(ctx context.Context, r *http.Request) (uuid.UUID, error) {
	orgIDParam := r.URL.Query().Get(api.OrganizationIDQueryKey)
	if orgIDParam == "" {
		return uuid.Nil, nil
	}

	parsedID, err := uuid.Parse(orgIDParam)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid %s parameter: %w", api.OrganizationIDQueryKey, err)
	}
	return parsedID, nil
}

// extractOrgIDFromRequestCert extracts organization ID from the client certificate.
// Returns the nil UUID if no organization ID is found in the certificate or if the
// certificate doesn't contain an org ID extension.
func extractOrgIDFromRequestCert(ctx context.Context, r *http.Request) (uuid.UUID, error) {
	peerCertificate, err := signer.PeerCertificateFromCtx(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to extract peer certificate from context: %w", err)
	}

	orgID, present, err := signer.GetOrgIDExtensionFromCert(peerCertificate)
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to extract organization ID from certificate: %w", err)
	}
	if !present {
		return uuid.Nil, fmt.Errorf("no organization ID found in certificate")
	}
	return orgID, nil
}

// SecurityHeaders adds security headers to all HTTP responses.
// This middleware should be applied early in the middleware chain to ensure
// all responses include these headers.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Strict-Transport-Security: Enforce HTTPS for 1 year including subdomains
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		// X-Content-Type-Options: Prevent MIME type sniffing
		w.Header().Set("X-Content-Type-Options", "nosniff")

		next.ServeHTTP(w, r)
	})
}
