package middleware

import (
	"context"

	"github.com/flightctl/flightctl/internal/auth"
	"github.com/flightctl/flightctl/internal/auth/common"
	middlewareMetadata "github.com/grpc-ecosystem/go-grpc-middleware/v2/metadata"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// client has to present either client TLS certificate or valid Authorization header
func GrpcAuthMiddleware(ctx context.Context) (context.Context, error) {
	authHeader := middlewareMetadata.ExtractIncoming(ctx).Get(common.AuthHeader)
	if authHeader == "" {
		return ValidateClientTlsCert(ctx)
	}
	authn := auth.GetAuthN()
	if _, ok := authn.(auth.NilAuth); ok {
		// auth disabled
		return ctx, nil
	}
	token, ok := auth.ParseAuthHeader(authHeader)
	if !ok {
		return ctx, status.Error(codes.Unauthenticated, "invalid authentication token")
	}
	ok, err := authn.ValidateToken(ctx, token)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, err.Error())
	}
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "invalid auth token")
	}
	return ctx, nil
}
