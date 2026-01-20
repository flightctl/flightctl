package transportv1beta1

import (
	"net/http"

	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/transport"
)

// (GET /api/v1/labels)
func (h *TransportHandler) ListLabels(w http.ResponseWriter, r *http.Request, params apiv1beta1.ListLabelsParams) {
	domainParams := h.converter.Common().ListLabelsParamsToDomain(params)
	body, status := h.serviceHandler.ListLabels(r.Context(), transport.OrgIDFromContext(r.Context()), domainParams)
	apiResult := h.converter.Common().LabelListFromDomain(body)
	transport.SetResponse(w, apiResult, status)
}
