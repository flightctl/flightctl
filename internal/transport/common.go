package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	convertv1alpha1 "github.com/flightctl/flightctl/internal/api/convert/v1alpha1"
	convertv1beta1 "github.com/flightctl/flightctl/internal/api/convert/v1beta1"
	"github.com/flightctl/flightctl/internal/api_server/versioning"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
)

const (
	Forbidden                      = "Forbidden"
	AuthorizationServerUnavailable = "Authorization server unavailable"
)

var (
	v1alpha1Common = convertv1alpha1.NewCommonConverter()
	v1beta1Common  = convertv1beta1.NewCommonConverter()
)

// WriteJSONErrorForVersion writes a version-correct JSON error response.
// Use this when the API version is known but no *http.Request is available
// (e.g. in OAPI error handler closures).
func WriteJSONErrorForVersion(w http.ResponseWriter, version versioning.Version, message string, statusCode int) {
	domainStatus := domain.NewFailureStatus(int32(statusCode), http.StatusText(statusCode), message) // #nosec G115 -- safe: HTTP status codes fit in int32
	var apiStatus any
	switch version {
	case versioning.V1Alpha1:
		apiStatus = v1alpha1Common.StatusFromDomain(domainStatus)
	default:
		apiStatus = v1beta1Common.StatusFromDomain(domainStatus)
	}
	resp, err := json.Marshal(apiStatus)
	if err != nil {
		http.Error(w, message, statusCode)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_, _ = w.Write(resp)
}

// WriteJSONError writes a JSON-formatted error response using the negotiated API version
// from the request context. If r is nil or no version is in context, defaults to v1beta1.
// This should be used instead of http.Error() to ensure all error responses are JSON.
func WriteJSONError(w http.ResponseWriter, r *http.Request, message string, statusCode int) {
	version := versioning.V1Beta1
	if r != nil {
		if v, ok := versioning.VersionFromContext(r.Context()); ok {
			version = v
		}
	}
	WriteJSONErrorForVersion(w, version, message, statusCode)
}

// WriteJSONResponse is a version-independent response writer.
// For 2xx status codes, body is encoded as the response. For non-2xx, errorBody
// is encoded instead. Responses with no body (204, 304, 1xx) only write the
// status code.
func WriteJSONResponse(w http.ResponseWriter, body any, errorBody any, code int) {
	// Never write a body for 204/304 (and generally 1xx), per RFC 7231
	if code == http.StatusNoContent || code == http.StatusNotModified || (code >= 100 && code < 200) {
		w.WriteHeader(code)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	hider, ok := body.(api.SensitiveDataHider)
	if ok {
		// If the body implements SensitiveDataHider, hide sensitive data before encoding
		if err := hider.HideSensitiveData(); err != nil {
			// If hiding sensitive data fails, return an internal server error
			WriteJSONError(w, nil, fmt.Sprintf("failed to hide sensitive data: %v", err), http.StatusInternalServerError)
			return
		}
	}

	// Encode body into a buffer first to catch encoding errors before writing the response
	var buf bytes.Buffer
	var err error

	if body != nil && code >= 200 && code < 300 {
		err = json.NewEncoder(&buf).Encode(body)
	} else {
		err = json.NewEncoder(&buf).Encode(errorBody)
	}

	if err != nil {
		WriteJSONError(w, nil, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(code)
	_, _ = w.Write(buf.Bytes())
}

// OrgIDFromContext extracts the organization ID from the context.
// Falls back to the default organization ID if not present.
func OrgIDFromContext(ctx context.Context) uuid.UUID {
	if orgID, ok := util.GetOrgIdFromContext(ctx); ok {
		return orgID
	}
	return store.NullOrgId
}
