package service

import (
	"context"

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
	// auth middleware already checked the token validity
	return server.AuthValidate200Response{}, nil
}
