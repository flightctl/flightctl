package transportv1beta1

import (
	"net/http"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/transport"
	"github.com/flightctl/flightctl/pkg/version"
)

// (GET /api/version)
func (h *TransportHandler) GetVersion(w http.ResponseWriter, r *http.Request) {
	versionInfo := version.Get()
	v := api.Version{
		Version: versionInfo.String(),
	}
	transport.SetResponse(w, v, api.StatusOK())
}
