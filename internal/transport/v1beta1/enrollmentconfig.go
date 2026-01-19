package transportv1beta1

import (
	"net/http"

	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/transport"
)

// (GET /api/v1/enrollmentconfig)
func (h *TransportHandler) GetEnrollmentConfig(w http.ResponseWriter, r *http.Request, params apiv1beta1.GetEnrollmentConfigParams) {
	domainParams := h.converter.EnrollmentRequest().GetConfigParamsToDomain(params)
	body, status := h.serviceHandler.GetEnrollmentConfig(r.Context(), transport.OrgIDFromContext(r.Context()), domainParams)
	apiResult := h.converter.EnrollmentRequest().ConfigFromDomain(body)
	transport.SetResponse(w, apiResult, status)
}
