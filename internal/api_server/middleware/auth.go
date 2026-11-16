package middleware

import (
	"context"

	"github.com/flightctl/flightctl/internal/crypto/signer"
	"github.com/flightctl/flightctl/internal/org"
	"github.com/flightctl/flightctl/internal/util"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// GrpcAuthMiddleware validates the client TLS certificate and extracts the
// organization ID from the certificate's OIDOrgID extension into the context.
// When the extension is absent the default organization is used, matching the
// REST path behaviour provided by ExtractAndValidateOrg(CertOrgIDExtractor).
// See https://github.com/flightctl/flightctl/issues/3910.
func GrpcAuthMiddleware(ctx context.Context) (context.Context, error) {
	ctx, err := ValidateClientTlsCert(ctx)
	if err != nil {
		return ctx, err
	}

	// Extract the verified peer certificate from the gRPC TLS info.
	p, ok := peer.FromContext(ctx)
	if !ok {
		return ctx, status.Error(codes.Unauthenticated, "no peer found")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(tlsInfo.State.VerifiedChains) == 0 || len(tlsInfo.State.VerifiedChains[0]) == 0 {
		return ctx, status.Error(codes.Unauthenticated, "failed to verify client certificate")
	}
	cert := tlsInfo.State.VerifiedChains[0][0]

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
