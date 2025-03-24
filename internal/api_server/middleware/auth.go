package middleware

import (
	"context"
)

// client has to present client TLS certificate
func GrpcAuthMiddleware(ctx context.Context) (context.Context, error) {
	return ValidateClientTlsCert(ctx)
}
