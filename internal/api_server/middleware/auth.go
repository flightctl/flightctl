package middleware

import (
	"context"

	"github.com/flightctl/flightctl/internal/crypto/signer"
	"github.com/flightctl/flightctl/internal/org"
	"github.com/flightctl/flightctl/internal/util"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GrpcAuthMiddleware requires a verified client TLS certificate and stores the
// organization ID from the certificate's OIDOrgID extension in the context.
// A certificate without the extension maps to the default organization,
// preserving the default gRPC handlers applied before this lookup existed.
// A certificate with a malformed extension is rejected as Unauthenticated.
func GrpcAuthMiddleware(ctx context.Context) (context.Context, error) {
	cert, err := verifiedClientCert(ctx)
	if err != nil {
		return ctx, err
	}

	orgID, present, err := signer.GetOrgIDExtensionFromCert(cert)
	if err != nil {
		return ctx, status.Errorf(codes.Unauthenticated, "failed to extract organization ID: %v", err)
	}
	if !present {
		orgID = org.DefaultID
	}
	ctx = util.WithOrganizationID(ctx, orgID)

	return ctx, nil
}
