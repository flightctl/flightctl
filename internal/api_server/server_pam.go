//go:build linux

package apiserver

import (
	"github.com/flightctl/flightctl/internal/auth/issuer"
)

// createPAMOIDCProvider creates an PAM OIDC provider
// This implementation is only available when building on Linux
func (s *Server) createPAMOIDCProvider() (issuer.OIDCIssuer, error) {
	return issuer.NewPAMOIDCProvider(s.ca, s.cfg.Auth.PAMOIDCIssuer)
}
