package common

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/identity"
)

// ContextKey is a custom type for context keys to avoid collisions
type ContextKey string

const (
	AuthHeader string = "Authorization"
)

type AuthOrganizationsConfig struct {
	Enabled bool
	// OrganizationAssignment defines how users are assigned to organizations
	OrganizationAssignment *v1alpha1.AuthOrganizationAssignment
}

type Identity interface {
	GetUsername() string
	GetUID() string
	GetOrganizations() []ReportedOrganization
	GetRoles() []string
	GetIssuer() *identity.Issuer
}
type ReportedOrganization struct {
	Name         string
	IsInternalID bool
	ID           string
}

type AuthNMiddleware interface {
	GetAuthToken(r *http.Request) (string, error)
	ValidateToken(ctx context.Context, token string) error
	GetIdentity(ctx context.Context, token string) (Identity, error)
	GetAuthConfig() *v1alpha1.AuthConfig
}

type BaseIdentity struct {
	username      string
	uID           string
	organizations []ReportedOrganization
	roles         []string
	issuer        *identity.Issuer
}

// Ensure BaseIdentity implements Identity
var _ Identity = (*BaseIdentity)(nil)

func NewBaseIdentity(username string, uID string, organizations []ReportedOrganization, roles []string) *BaseIdentity {
	return &BaseIdentity{
		username:      username,
		uID:           uID,
		organizations: organizations,
		roles:         roles,
		issuer:        nil, // Will be set by the identity provider
	}
}

func NewBaseIdentityWithIssuer(username string, uID string, organizations []ReportedOrganization, roles []string, issuer *identity.Issuer) *BaseIdentity {
	return &BaseIdentity{
		username:      username,
		uID:           uID,
		organizations: organizations,
		roles:         roles,
		issuer:        issuer,
	}
}

func (i *BaseIdentity) GetUsername() string {
	return i.username
}

func (i *BaseIdentity) SetUsername(username string) {
	i.username = username
}

func (i *BaseIdentity) GetUID() string {
	return i.uID
}

func (i *BaseIdentity) SetUID(uID string) {
	i.uID = uID
}

func (i *BaseIdentity) GetOrganizations() []ReportedOrganization {
	return i.organizations
}

func (i *BaseIdentity) SetOrganizations(organizations []ReportedOrganization) {
	i.organizations = organizations
}

func (i *BaseIdentity) GetRoles() []string {
	return append([]string(nil), i.roles...)
}

func (i *BaseIdentity) SetRoles(roles []string) {
	i.roles = append([]string(nil), roles...)
}

func (i *BaseIdentity) GetIssuer() *identity.Issuer {
	return i.issuer
}

func (i *BaseIdentity) SetIssuer(issuer *identity.Issuer) {
	i.issuer = issuer
}

func GetIdentity(ctx context.Context) (Identity, error) {
	identityVal := ctx.Value(consts.IdentityCtxKey)
	if identityVal == nil {
		return nil, fmt.Errorf("failed to get identity from context")
	}
	identity, ok := identityVal.(Identity)
	if !ok {
		return nil, fmt.Errorf("incorrect type of identity in context (got %T)", identityVal)
	}
	return identity, nil
}

func ExtractBearerToken(r *http.Request) (string, error) {
	authHeader := r.Header.Get(AuthHeader)
	if authHeader == "" {
		return "", fmt.Errorf("empty %s header", AuthHeader)
	}
	token := strings.TrimPrefix(authHeader, "Bearer ")
	if token == authHeader {
		return "", fmt.Errorf("invalid %s header", AuthHeader)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", fmt.Errorf("invalid token")
	}
	return token, nil
}

// IsPublicAuthEndpoint checks if the given path is a public auth endpoint that doesn't require authentication or org validation.
// Only includes endpoints served by the main API server. OIDC/OAuth2 endpoints (authorize, token, jwks, etc.)
// are served by the PAM issuer on a separate server and are not included here.
func IsPublicAuthEndpoint(path string) bool {
	if path == "/api/v1/auth/config" {
		return true
	}
	// Match /api/v1/auth/{providername}/token pattern
	if strings.HasPrefix(path, "/api/v1/auth/") && strings.HasSuffix(path, "/token") {
		return true
	}
	return false
}
