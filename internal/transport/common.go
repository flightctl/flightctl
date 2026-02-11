package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
)

const (
	Forbidden                      = "Forbidden"
	AuthorizationServerUnavailable = "Authorization server unavailable"
)

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
			http.Error(w, fmt.Sprintf("failed to hide sensitive data: %v", err), http.StatusInternalServerError)
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
