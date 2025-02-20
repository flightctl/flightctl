package service

import (
	"context"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/auth"
)

// (GET /api/v1/auth/config)
func (h *ServiceHandler) AuthConfig(ctx context.Context, request server.AuthConfigRequestObject) (server.AuthConfigResponseObject, error) {
	authN := auth.GetAuthN()
	if _, ok := authN.(auth.NilAuth); ok {
		return server.AuthConfig418Response{}, nil
	}

	authConfig := authN.GetAuthConfig()

	return server.AuthConfig200JSONResponse{
		AuthType: authConfig.Type,
		AuthURL:  authConfig.Url,
	}, nil
}

// (GET /api/v1/auth/validate)
func (h *ServiceHandler) AuthValidate(ctx context.Context, request server.AuthValidateRequestObject) (server.AuthValidateResponseObject, error) {
	authn := auth.GetAuthN()
	if _, ok := authn.(auth.NilAuth); ok {
		return server.AuthValidate418Response{}, nil
	}
	if request.Params.Authorization == nil {
		return server.AuthValidate401Response{}, nil
	}
	token, ok := auth.ParseAuthHeader(*request.Params.Authorization)
	if !ok {
		return server.AuthValidate401Response{}, nil
	}
	valid, err := authn.ValidateToken(ctx, token)
	if err != nil {
		return server.AuthValidate500JSONResponse(api.StatusInternalServerError(err.Error())), nil
	}
	if !valid {
		return server.AuthValidate401Response{}, nil
	}
	return server.AuthValidate200Response{}, nil
}
