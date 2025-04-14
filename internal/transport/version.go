package transport

import (
	"net/http"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/instrumentation"
	"github.com/flightctl/flightctl/pkg/version"
)

// (GET /api/version)
func (h *TransportHandler) GetVersion(w http.ResponseWriter, r *http.Request) {
	_, span := instrumentation.StartSpan(r.Context(), "flightctl/transport", "TransportHandler.GetVersion")
	defer span.End()

	versionInfo := version.Get()
	v := api.Version{
		Version: versionInfo.String(),
	}
	SetResponse(w, v, api.StatusOK())
}
