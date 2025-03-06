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
	SetResponse(w, conf, api.StatusOK())
}

// (GET /api/v1/auth/validate)
func (h *TransportHandler) AuthValidate(w http.ResponseWriter, r *http.Request, params api.AuthValidateParams) {
	authn := auth.GetAuthN()
	if _, ok := authn.(auth.NilAuth); ok {
		SetResponse(w, nil, api.StatusAuthNotConfigured("Auth not configured"))
		return
	}
	if params.Authorization == nil {
		SetResponse(w, nil, api.StatusUnauthorized("Authorization not specified"))
		return
	}
	token, ok := auth.ParseAuthHeader(*params.Authorization)
	if !ok {
		SetResponse(w, nil, api.StatusUnauthorized("Failed parsing authorization"))
		return
	}
	valid, err := authn.ValidateToken(r.Context(), token)
	if err != nil {
		SetResponse(w, nil, api.StatusInternalServerError(err.Error()))
		return
	}
	if !valid {
		SetResponse(w, nil, api.StatusUnauthorized(http.StatusText(http.StatusUnauthorized)))
		return
	}
	SetResponse(w, nil, api.StatusOK())
}
