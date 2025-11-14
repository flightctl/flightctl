package transport

import (
	"encoding/json"
	"net/http"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/auth"
)

// AuthCheckPermission checks multiple permissions for the authenticated user
// (POST /api/v1/auth/checkpermission)
func (h *TransportHandler) AuthCheckPermission(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Check if auth is configured
	if _, ok := h.authN.(auth.NilAuth); ok {
		SetResponse(w, nil, api.StatusAuthNotConfigured("Auth not configured"))
		return
	}

	// Parse request body
	var request api.PermissionCheckRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		SetParseFailureResponse(w, err)
		return
	}

	// Validate request
	if len(request.Permissions) == 0 {
		SetResponse(w, nil, api.StatusBadRequest("permissions list cannot be empty"))
		return
	}

	// Check each permission
	results := make([]api.PermissionCheckResult, len(request.Permissions))
	for i, item := range request.Permissions {
		// Perform permission check using authZ
		allowed, err := h.authZ.CheckPermission(r.Context(), item.Resource, item.Op)
		if err != nil {
			// If there's an error checking permission, treat it as denied
			allowed = false
		}

		results[i] = api.PermissionCheckResult{
			Resource: item.Resource,
			Op:       item.Op,
			Allowed:  allowed,
		}
	}

	// Return response
	response := api.PermissionCheckResponse{
		Results: results,
	}

	SetResponse(w, &response, api.StatusOK())
}
