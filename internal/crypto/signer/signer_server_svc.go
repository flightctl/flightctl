package signer

import (
	"context"
	"fmt"

	"github.com/sirupsen/logrus"

	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	authcommon "github.com/flightctl/flightctl/internal/auth/common"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
)

const signerServerSvcExpiryDays int32 = 365

type SignerServerSvc struct {
	name string
	ca   CA
}

func NewSignerServerSvc(CAClient CA) Signer {
	cfg := CAClient.Config()
	return &SignerServerSvc{name: cfg.ServerSvcSignerName, ca: CAClient}
}

func (s *SignerServerSvc) Name() string {
	return s.name
}

func (s *SignerServerSvc) Verify(ctx context.Context, request api.CertificateSigningRequest) error {
	// For server service certificates, we require CLI authentication
	// Users must authenticate through the CLI (same as other operations)
	identity, err := authcommon.GetIdentity(ctx)
	if err != nil {
		return fmt.Errorf("server service certificate requests require CLI authentication: %w", err)
	}
	// Validate that the authenticated user has appropriate permissions
	if identity != nil && identity.Username != "" {
		// Restrict server certificate requests to specific roles
		// Only allow users with admin or installer roles to request server certificates
		allowedRoles := []string{"flightctl-admin", "flightctl-installer"}
		hasAllowedRole := false

		for _, role := range allowedRoles {
			for _, userRole := range identity.Groups {
				if userRole == role {
					hasAllowedRole = true
					break
				}
			}
			if hasAllowedRole {
				break
			}
		}

		if !hasAllowedRole {
			return fmt.Errorf("user %s is not authorized to request server certificates. Required roles: %v",
				identity.Username, allowedRoles)
		}
	}

	parsedCSR, err := fccrypto.ParseCSR(request.Spec.Request)
	if err != nil {
		return fmt.Errorf("failed to parse CSR: %w", err)
	}

	// For server certificates, we expect the CN to be a service name with svc- prefix
	if parsedCSR.Subject.CommonName == "" {
		return fmt.Errorf("CSR CommonName cannot be empty for server certificates")
	}

	// Validate that the CN starts with the expected service prefix
	servicePrefix := "svc-"
	if !strings.HasPrefix(parsedCSR.Subject.CommonName, servicePrefix) {
		return fmt.Errorf("CSR CommonName %q must start with prefix %q", parsedCSR.Subject.CommonName, servicePrefix)
	}

	// Extract service name and validate it's not empty
	serviceName := strings.TrimPrefix(parsedCSR.Subject.CommonName, servicePrefix)
	if serviceName == "" {
		return fmt.Errorf("CSR CommonName %q must include a service name after prefix %q", parsedCSR.Subject.CommonName, servicePrefix)
	}

	return nil
}

func (s *SignerServerSvc) Sign(ctx context.Context, request api.CertificateSigningRequest) ([]byte, error) {
	cert, err := fccrypto.ParseCSR(request.Spec.Request)
	if err != nil {
		return nil, err
	}

	// Ensure the CommonName follows the service naming convention
	servicePrefix := "svc-"
	if !strings.HasPrefix(cert.Subject.CommonName, servicePrefix) {
		return nil, fmt.Errorf("CSR CommonName %q must start with prefix %q", cert.Subject.CommonName, servicePrefix)
	}

	serviceName := strings.TrimPrefix(cert.Subject.CommonName, servicePrefix)
	if serviceName == "" {
		return nil, fmt.Errorf("CSR CommonName %q must include a service name after prefix %q", cert.Subject.CommonName, servicePrefix)
	}

	expirySeconds := signerServerSvcExpiryDays * 24 * 60 * 60
	if request.Spec.ExpirationSeconds != nil && *request.Spec.ExpirationSeconds < expirySeconds {
		expirySeconds = *request.Spec.ExpirationSeconds
	}

	opts := []certOption{
		WithExtension(OIDOrgID, NullOrgId.String()),
	}
	res, err := s.ca.IssueRequestedServerOnlyCertificate(
		ctx,
		cert,
		int(expirySeconds),
		opts...,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to issue server certificate: %w", err)
	}
	logrus.Infof("issued server certificate for service %s with %d bytes", serviceName, len(res))
	return res, nil
}
