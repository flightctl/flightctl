//go:build linux

package issuer

import (
	"os/user"

	"github.com/flightctl/flightctl/internal/auth/authn"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/sirupsen/logrus"
)

// Logger is a type alias for logrus.Logger
type Logger = *logrus.Logger

// SSSDOIDCProvider represents an SSSD-based OIDC issuer
type SSSDOIDCProvider struct {
	jwtGenerator      *authn.JWTGenerator
	config            *config.SSSDOIDCIssuer
	sssdAuthenticator SSSDAuthenticator
	codeStore         *AuthorizationCodeStore
	sessionStore      *SessionStore
	log               Logger
}

// SSSDAuthenticator interface for SSSD authentication and user lookup
type SSSDAuthenticator interface {
	Authenticate(username, password string) error
	LookupUser(username string) (*user.User, error)
	GetUserGroups(systemUser *user.User) ([]string, error)
	Close() error
}
