package common

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/identity"
)

// ContextKey is a custom type for context keys to avoid collisions
type ContextKey string

const (
	AuthHeader string = "Authorization"
)

type AuthOrganizationsConfig struct {
	// OrganizationAssignment defines how users are assigned to organizations
	OrganizationAssignment *v1beta1.AuthOrganizationAssignment
}

type Identity interface {
	GetUsername() string
	GetUID() string
	GetOrganizations() []ReportedOrganization
	GetIssuer() *identity.Issuer
	IsSuperAdmin() bool
	SetSuperAdmin(bool)
}

// K8sIdentityProvider extends Identity with control plane URL for K8s-based auth
type K8sIdentityProvider interface {
	Identity
	GetControlPlaneUrl() string
}
type ReportedOrganization struct {
	Name         string
	IsInternalID bool
	ID           string
	Roles        []string
}

type AuthNMiddleware interface {
	GetAuthToken(r *http.Request) (string, error)
	ValidateToken(ctx context.Context, token string) error
	GetIdentity(ctx context.Context, token string) (Identity, error)
	GetAuthConfig() *v1beta1.AuthConfig
	IsEnabled() bool
}
type MultiAuthNMiddleware interface {
	AuthNMiddleware
	ValidateTokenAndGetProvider(ctx context.Context, token string) (AuthNMiddleware, error)
}

type BaseIdentity struct {
	username      string
	uID           string
	organizations []ReportedOrganization
	issuer        *identity.Issuer
	superAdmin    bool
}

// Ensure BaseIdentity implements Identity
var _ Identity = (*BaseIdentity)(nil)

func NewBaseIdentity(username string, uID string, organizations []ReportedOrganization) *BaseIdentity {
	return &BaseIdentity{
		username:      username,
		uID:           uID,
		organizations: organizations,
		issuer:        nil, // Will be set by the identity provider
	}
}

func NewBaseIdentityWithIssuer(username string, uID string, organizations []ReportedOrganization, issuer *identity.Issuer) *BaseIdentity {
	return &BaseIdentity{
		username:      username,
		uID:           uID,
		organizations: organizations,
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

func (i *BaseIdentity) GetIssuer() *identity.Issuer {
	return i.issuer
}

func (i *BaseIdentity) SetIssuer(issuer *identity.Issuer) {
	i.issuer = issuer
}

func (i *BaseIdentity) IsSuperAdmin() bool {
	return i.superAdmin
}

func (i *BaseIdentity) SetSuperAdmin(superAdmin bool) {
	i.superAdmin = superAdmin
}

// K8sIdentity extends BaseIdentity with K8s control plane URL and RBAC namespace
type K8sIdentity struct {
	*BaseIdentity
	controlPlaneUrl string
	rbacNs          string
}

// Ensure K8sIdentity implements K8sIdentityProvider
var _ K8sIdentityProvider = (*K8sIdentity)(nil)

func NewK8sIdentity(username string, uID string, organizations []ReportedOrganization, issuer *identity.Issuer, controlPlaneUrl string, rbacNs string) *K8sIdentity {
	return &K8sIdentity{
		BaseIdentity:    NewBaseIdentityWithIssuer(username, uID, organizations, issuer),
		controlPlaneUrl: controlPlaneUrl,
		rbacNs:          rbacNs,
	}
}

func (i *K8sIdentity) GetControlPlaneUrl() string {
	return i.controlPlaneUrl
}

func (i *K8sIdentity) GetRbacNs() string {
	return i.rbacNs
}

// OpenShiftIdentity extends BaseIdentity with OpenShift control plane URL
type OpenShiftIdentity struct {
	*BaseIdentity
	controlPlaneUrl string
}

// Ensure OpenShiftIdentity implements K8sIdentityProvider
var _ K8sIdentityProvider = (*OpenShiftIdentity)(nil)

func NewOpenShiftIdentity(username string, uID string, organizations []ReportedOrganization, issuer *identity.Issuer, controlPlaneUrl string) *OpenShiftIdentity {
	return &OpenShiftIdentity{
		BaseIdentity:    NewBaseIdentityWithIssuer(username, uID, organizations, issuer),
		controlPlaneUrl: controlPlaneUrl,
	}
}

func (i *OpenShiftIdentity) GetControlPlaneUrl() string {
	return i.controlPlaneUrl
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

// ShouldValidateOrg checks if org validation should be performed for the given method and path.
// Returns false if org validation should be skipped, true otherwise.
func ShouldValidateOrg(method, path string) bool {
	// Skip org validation for public auth endpoints
	if IsPublicAuthEndpoint(path) {
		return false
	}

	// Normalize path by removing trailing slash
	normalizedPath := strings.TrimSuffix(path, "/")

	// Skip org validation for GET /api/v1/organizations endpoint
	if method == http.MethodGet && normalizedPath == "/api/v1/organizations" {
		return false
	}

	// Skip org validation for GET /api/v1/auth/validate endpoint
	if method == http.MethodGet && normalizedPath == "/api/v1/auth/validate" {
		return false
	}

	// Skip org validation for GET /api/v1/auth/userinfo endpoint
	if method == http.MethodGet && normalizedPath == "/api/v1/auth/userinfo" {
		return false
	}

	return true
}

// BuildReportedOrganizations creates ReportedOrganization list from organizations and their roles
// It handles:
// - Extracting global roles (from "*" key in orgRoles map)
// - Detecting flightctl-admin role and setting super admin flag
// - Filtering out flightctl-admin from both global and org-specific roles (it's only used for super admin flag)
// - Distributing remaining global roles to all organizations
// - Combining org-specific and global roles for each organization
func BuildReportedOrganizations(organizations []string, orgRoles map[string][]string, isInternalID bool) ([]ReportedOrganization, bool) {
	reportedOrganizations := make([]ReportedOrganization, 0, len(organizations))
	globalRoles := orgRoles["*"] // Get global roles if any

	// Build filtered global roles map and check for flightctl-admin
	isSuperAdmin := false
	filteredGlobalRolesMap := make(map[string]struct{}, len(globalRoles))
	for _, role := range globalRoles {
		if role == v1beta1.ExternalRoleAdmin {
			isSuperAdmin = true
			filteredGlobalRolesMap[v1beta1.ExternalRoleOrgAdmin] = struct{}{}
		} else {
			filteredGlobalRolesMap[role] = struct{}{}
		}
	}

	// Build reported organizations with roles
	for _, org := range organizations {
		// Use a map to deduplicate roles from the start
		roleSet := make(map[string]struct{}, len(orgRoles[org])+len(filteredGlobalRolesMap))

		// Add org-specific roles (filter out flightctl-admin)
		for _, role := range orgRoles[org] {
			if role != v1beta1.ExternalRoleAdmin {
				roleSet[role] = struct{}{}
			}
		}

		// Add global roles (already filtered)
		for role := range filteredGlobalRolesMap {
			roleSet[role] = struct{}{}
		}

		// Convert map to slice
		allRoles := make([]string, 0, len(roleSet))
		for role := range roleSet {
			allRoles = append(allRoles, role)
		}
		// Sort roles to ensure consistent ordering
		sort.Strings(allRoles)

		reportedOrganizations = append(reportedOrganizations, ReportedOrganization{
			Name:         org,
			IsInternalID: isInternalID,
			ID:           org,
			Roles:        allRoles,
		})
	}

	return reportedOrganizations, isSuperAdmin
}
