package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
)

const (
	Forbidden                      = "Forbidden"
	AuthorizationServerUnavailable = "Authorization server unavailable"
)

func SetResponse(w http.ResponseWriter, body any, status api.Status) {
	code := int(status.Code)

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

	if body != nil && status.Code >= 200 && status.Code < 300 {
		err = json.NewEncoder(&buf).Encode(body)
	} else {
		err = json.NewEncoder(&buf).Encode(status)
	}

	if err != nil {
		// If encoding fails, send an internal server error response
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Now that encoding is successful, write the status and response
	w.WriteHeader(code)
	_, _ = w.Write(buf.Bytes()) // Write the encoded JSON from the buffer
}

func SetParseFailureResponse(w http.ResponseWriter, err error) {
	SetResponse(w, nil, api.StatusInternalServerError(fmt.Sprintf("can't decode JSON body: %v", err)))
}

// OrgIDFromContext extracts the organization ID from the context.
// Falls back to the default organization ID if not present.
func OrgIDFromContext(ctx context.Context) uuid.UUID {
	if orgID, ok := util.GetOrgIdFromContext(ctx); ok {
		return orgID
	}
	return store.NullOrgId
}
