package transportv1beta1

import (
	"net/http"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
)

// AuthConfig returns the authentication configuration
// (GET /api/v1/auth/config)
func (h *TransportHandler) AuthConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	authConfig := h.authN.GetAuthConfig()
	body, status := h.serviceHandler.GetAuthConfig(r.Context(), authConfig)
	h.SetResponse(w, body, status)
}

// AuthValidate validates an authentication token
// (GET /api/v1/auth/validate)
func (h *TransportHandler) AuthValidate(w http.ResponseWriter, r *http.Request, params api.AuthValidateParams) {
	// auth middleware already checked the token validity
	h.SetResponse(w, nil, api.StatusOK())
}
