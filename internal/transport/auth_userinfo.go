package transport

import (
	"net/http"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/auth"
)

// AuthUserInfo handles UserInfo requests
// (GET /api/v1/auth/userinfo)
func (h *TransportHandler) AuthUserInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Check if auth is configured
	if _, ok := h.authN.(auth.NilAuth); ok {
		SetResponse(w, nil, api.StatusAuthNotConfigured("Auth not configured"))
		return
	}

	// Check if userinfo proxy is configured
	if h.authUserInfoProxy == nil {
		SetResponse(w, nil, api.StatusAuthNotConfigured("UserInfo proxy not configured"))
		return
	}

	// Extract identity from context and return userinfo
	// (identity is set by auth middleware after token validation)
	userInfo, status := h.authUserInfoProxy.ProxyUserInfoRequest(r.Context())
	SetResponse(w, userInfo, status)
}
