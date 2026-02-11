package transportv1beta1

import (
	"net/http"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
)

// AuthGetPermissions returns the list of available permissions for the authenticated user
// (GET /api/v1/auth/permissions)
func (h *TransportHandler) AuthGetPermissions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Get all available permissions for the user
	permissionList, err := h.authZ.GetUserPermissions(r.Context())
	if err != nil {
		h.SetResponse(w, nil, api.StatusInternalServerError(err.Error()))
		return
	}

	// Return response
	h.SetResponse(w, permissionList, api.StatusOK())
}
