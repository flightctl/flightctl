package transportv1beta1

import (
	"net/http"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/transport"
)

// AuthUserInfo handles UserInfo requests
// (GET /api/v1/auth/userinfo)
func (h *TransportHandler) AuthUserInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Check if userinfo proxy is configured
	if h.authUserInfoProxy == nil {
		transport.SetResponse(w, nil, api.StatusAuthNotConfigured("UserInfo proxy not configured"))
		return
	}

	// Extract identity from context and return userinfo
	// (identity is set by auth middleware after token validation)
	userInfo, status := h.authUserInfoProxy.ProxyUserInfoRequest(r.Context())
	transport.SetResponse(w, userInfo, status)
}
