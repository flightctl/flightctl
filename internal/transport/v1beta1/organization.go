package transportv1beta1

import (
	"net/http"

	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/transport"
)

// (GET /api/v1/organizations)
func (h *TransportHandler) ListOrganizations(w http.ResponseWriter, r *http.Request, params apiv1beta1.ListOrganizationsParams) {
	domainParams := h.converter.V1beta1().Organization().ListParamsToDomain(params)
	body, status := h.serviceHandler.ListOrganizations(r.Context(), domainParams)
	apiResult := h.converter.V1beta1().Organization().ListFromDomain(body)
	transport.SetResponse(w, apiResult, status)
}
