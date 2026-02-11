//go:build linux

package pam

import (
	"html/template"
	"os/user"

	"github.com/flightctl/flightctl/internal/auth/authn"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/sirupsen/logrus"
)

// Logger is a type alias for logrus.Logger
type Logger = *logrus.Logger

// PAMOIDCProvider represents a PAM-based OIDC issuer
type PAMOIDCProvider struct {
	jwtGenerator      *authn.JWTGenerator
	config            *config.PAMOIDCIssuer
	pamAuthenticator  Authenticator
	codeStore         *AuthorizationCodeStore
	log               Logger
	loginFormTemplate *template.Template
	cookieKey         []byte // AES-256 key for encrypting pending auth cookies
}

// Authenticator interface for PAM authentication and NSS user lookup
type Authenticator interface {
	Authenticate(username, password string) error
	LookupUser(username string) (*user.User, error)
	GetUserGroups(systemUser *user.User) ([]string, error)
	Close() error
}
