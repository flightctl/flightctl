package transport

import (
	"net/http"

	api "github.com/flightctl/flightctl/api/v1beta1"
)

// (GET /api/v1/enrollmentconfig)
func (h *TransportHandler) GetEnrollmentConfig(w http.ResponseWriter, r *http.Request, params api.GetEnrollmentConfigParams) {
	body, status := h.serviceHandler.GetEnrollmentConfig(r.Context(), OrgIDFromContext(r.Context()), params)
	SetResponse(w, body, status)
}
