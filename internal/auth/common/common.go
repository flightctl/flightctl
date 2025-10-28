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

const (
	AuthHeader string = "Authorization"
)

type AuthOrganizationsConfig struct {
	Enabled bool
	// OrganizationAssignment defines how users are assigned to organizations
	OrganizationAssignment *OrganizationAssignment
}

// OrganizationAssignment defines how users are assigned to organizations
type OrganizationAssignment struct {
	Type string `json:"type"` // "static", "dynamic", or "perUser"

	// Static assignment fields
	OrganizationName *string `json:"organizationName,omitempty"`

	// Dynamic assignment fields
	ClaimPath              *string `json:"claimPath,omitempty"`
	OrganizationNamePrefix *string `json:"organizationNamePrefix,omitempty"`
	OrganizationNameSuffix *string `json:"organizationNameSuffix,omitempty"`
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
