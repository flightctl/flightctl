package transport

import (
	"net/http"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/auth"
)

// (GET /api/v1/auth/config)
func (h *TransportHandler) AuthConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	authN := auth.GetAuthN()
	if _, ok := authN.(auth.NilAuth); ok {
		SetResponse(w, nil, api.StatusAuthNotConfigured("Auth not configured"))
		return
	}

	authConfig := authN.GetAuthConfig()

	conf := api.AuthConfig{
		AuthType: authConfig.Type,
		AuthURL:  authConfig.Url,
	}

	if authConfig.ClientId != "" {
		conf.ClientId = &authConfig.ClientId
	}
	if authConfig.Scope != "" {
		conf.Scope = &authConfig.Scope
	}
	if authConfig.ForcePKCE {
		conf.ForcePKCE = &authConfig.ForcePKCE
	}
	SetResponse(w, conf, api.StatusOK())
}

// (GET /api/v1/auth/validate)
func (h *TransportHandler) AuthValidate(w http.ResponseWriter, r *http.Request, params api.AuthValidateParams) {
	// auth middleware already checked the token validity
	SetResponse(w, nil, api.StatusOK())
}
