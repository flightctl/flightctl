package middleware

import (
	"context"
	"fmt"
	"net/http"

	api "github.com/flightctl/flightctl/api/v1beta1"
	authcommon "github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/contextutil"
	"github.com/flightctl/flightctl/internal/crypto/signer"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/identity"
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
		identity, ok := contextutil.GetMappedIdentityFromContext(ctx)
		if ok && identity != nil {
			userName = identity.GetUsername()
		}
		ctx = context.WithValue(ctx, consts.EventActorCtxKey, fmt.Sprintf("user:%s", userName))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// UserAgentLogger logs the User-Agent header from incoming requests
// and sets it in the request context.
func UserAgentLogger(logger logrus.FieldLogger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			userAgent := r.Header.Get("User-Agent")
			logger.Debugf("UserAgentLogger: User-Agent from request=%q", userAgent)
			ctx = util.WithUserAgent(ctx, userAgent)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// OrgIDExtractor extracts an organization ID from an HTTP request.
// Returns (orgID, present, error) where present is true if the org ID was
// explicitly specified in the request.
type OrgIDExtractor func(context.Context, *http.Request) (uuid.UUID, bool, error)

// QueryOrgIDExtractor is the default extractor that reads the org_id from the query string.
var QueryOrgIDExtractor OrgIDExtractor = extractOrgIDFromRequestQuery

// CertOrgIDExtractor reads the org_id from the client certificate.
var CertOrgIDExtractor OrgIDExtractor = extractOrgIDFromRequestCert

// ExtractAndValidateOrg extracts organization ID using the supplied extractor, validates
// membership, and sets it in the request context.
func ExtractAndValidateOrg(extractor OrgIDExtractor, logger logrus.FieldLogger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !authcommon.ShouldValidateOrg(r.Method, r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			ctx := r.Context()
			reqLogger := log.WithReqIDFromCtx(ctx, logger)

			mappedIdentity, ok := contextutil.GetMappedIdentityFromContext(ctx)
			if !ok {
				http.Error(w, flterrors.ErrNoMappedIdentity.Error(), http.StatusInternalServerError)
				return
			}

			orgID, err := resolveOrgID(ctx, r, extractor, mappedIdentity)
			if err != nil {
				reqLogger.Debugf("ExtractAndValidateOrg: error resolving org: %v", err)
				http.Error(w, err.Error(), statusForOrgError(err))
				return
			}

			reqLogger.Debugf("ExtractAndValidateOrg: resolved orgID=%s", orgID.String())
			ctx = util.WithOrganizationID(ctx, orgID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// resolveOrgID extracts an org ID from the request or infers it from the user's orgs.
// If explicitly provided, validates the user is a member.
func resolveOrgID(ctx context.Context, r *http.Request, extractor OrgIDExtractor, mappedIdentity *identity.MappedIdentity) (uuid.UUID, error) {
	orgID, present, err := extractor(ctx, r)
	if err != nil {
		return uuid.Nil, err
	}

	userOrgs := mappedIdentity.GetOrganizations()
	if present {
		for _, org := range userOrgs {
			if org.ID == orgID {
				return orgID, nil
			}
		}
		return uuid.Nil, flterrors.ErrNotOrgMember
	}

	// No org ID provided from the extrctor
	// If the user only has access to one org, return that org ID
	// If the user has no orgs or multiple orgs without one specified, return an error
	switch len(userOrgs) {
	case 1:
		return userOrgs[0].ID, nil
	case 0:
		return uuid.Nil, flterrors.ErrNoOrganizations
	default:
		return uuid.Nil, flterrors.ErrAmbiguousOrganization
	}
}

func statusForOrgError(err error) int {
	switch err {
	case flterrors.ErrNoOrganizations, flterrors.ErrNotOrgMember:
		return http.StatusForbidden
	case flterrors.ErrAmbiguousOrganization, flterrors.ErrInvalidOrgID:
		return http.StatusBadRequest
	}
	return http.StatusBadRequest
}

func extractOrgIDFromRequestQuery(ctx context.Context, r *http.Request) (uuid.UUID, bool, error) {
	orgIDParam := r.URL.Query().Get(api.OrganizationIDQueryKey)
	if orgIDParam == "" {
		return uuid.Nil, false, nil
	}

	parsedID, err := uuid.Parse(orgIDParam)
	if err != nil {
		return uuid.Nil, false, flterrors.ErrInvalidOrgID
	}
	return parsedID, true, nil
}

// extractOrgIDFromRequestCert extracts organization ID from the client certificate.
// Returns (orgID, true, nil) if an org ID is found in the certificate.
// Returns (uuid.Nil, false, error) if no certificate is found or has no org ID extension.
func extractOrgIDFromRequestCert(ctx context.Context, r *http.Request) (uuid.UUID, bool, error) {
	peerCertificate, err := signer.PeerCertificateFromCtx(ctx)
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("failed to extract peer certificate from context: %w", err)
	}

	orgID, present, err := signer.GetOrgIDExtensionFromCert(peerCertificate)
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("failed to extract organization ID from certificate: %w", err)
	}
	if !present {
		return uuid.Nil, false, fmt.Errorf("no organization ID found in certificate")
	}
	return orgID, true, nil
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
