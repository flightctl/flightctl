package service

import (
	"context"

	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/auth"
)

// (GET /api/v1/token/request)
func (h *ServiceHandler) TokenRequest(ctx context.Context, request server.TokenRequestRequestObject) (server.TokenRequestResponseObject, error) {
	authn := auth.GetAuthN()
	if _, ok := authn.(auth.NilAuth); ok {
		return server.TokenRequest418Response{}, nil
	}

	url, err := authn.GetTokenRequestURL(ctx)
	if err != nil {
		return nil, err
	}

	return server.TokenRequest301Response{
		Headers: server.TokenRequest301ResponseHeaders{
			Location: url,
		},
	}, nil
}

// (GET /api/v1/token/validate)
func (h *ServiceHandler) TokenValidate(ctx context.Context, request server.TokenValidateRequestObject) (server.TokenValidateResponseObject, error) {
	authn := auth.GetAuthN()
	if _, ok := authn.(auth.NilAuth); ok {
		return server.TokenValidate418Response{}, nil
	}
	if request.Params.Authentication == nil {
		return server.TokenValidate401Response{}, nil
	}
	token, ok := auth.ParseAuthHeader(*request.Params.Authentication)
	if !ok {
		return server.TokenValidate401Response{}, nil
	}
	valid, err := authn.ValidateToken(ctx, token)
	if err != nil {
		return nil, err
	}
	if !valid {
		return server.TokenValidate401Response{}, nil
	}
	return server.TokenValidate200Response{}, nil
}
