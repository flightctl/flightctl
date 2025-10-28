//go:build linux

package apiserver

import (
	"github.com/flightctl/flightctl/internal/auth/issuer"
)

// createSSSDOIDCProvider creates an SSSD OIDC provider
// This implementation is only available when building on Linux
func (s *Server) createSSSDOIDCProvider() (issuer.OIDCIssuer, error) {
	return issuer.NewSSSDOIDCProvider(s.ca, s.cfg.Auth.SSSDOIDCIssuer)
}
