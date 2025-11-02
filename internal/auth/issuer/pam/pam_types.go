//go:build linux

package pam

import (
	"os/user"

	"github.com/flightctl/flightctl/internal/auth/authn"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/sirupsen/logrus"
)

// Logger is a type alias for logrus.Logger
type Logger = *logrus.Logger

// PAMOIDCProvider represents a PAM-based OIDC issuer
type PAMOIDCProvider struct {
	jwtGenerator     *authn.JWTGenerator
	config           *config.PAMOIDCIssuer
	pamAuthenticator PAMAuthenticator
	codeStore        *AuthorizationCodeStore
	sessionStore     *SessionStore
	log              Logger
}

// PAMAuthenticator interface for PAM authentication and NSS user lookup
type PAMAuthenticator interface {
	Authenticate(username, password string) error
	LookupUser(username string) (*user.User, error)
	GetUserGroups(systemUser *user.User) ([]string, error)
	Close() error
}
