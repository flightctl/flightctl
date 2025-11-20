package authn

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/sirupsen/logrus"
)

// OAuth2Auth implements OAuth2 authentication using userinfo endpoint validation
type OAuth2Auth struct {
	metadata              api.ObjectMeta
	spec                  api.OAuth2ProviderSpec
	tlsConfig             *tls.Config
	orgConfig             *common.AuthOrganizationsConfig
	roleExtractor         *RoleExtractor
	log                   logrus.FieldLogger
	organizationExtractor *OrganizationExtractor
}

// NewOAuth2Auth creates a new OAuth2 authentication instance
func NewOAuth2Auth(metadata api.ObjectMeta, spec api.OAuth2ProviderSpec, tlsConfig *tls.Config, log logrus.FieldLogger) (*OAuth2Auth, error) {
	if spec.AuthorizationUrl == "" {
		return nil, fmt.Errorf("authorizationUrl is required")
	}
	if spec.TokenUrl == "" {
		return nil, fmt.Errorf("tokenUrl is required")
	}
	if spec.UserinfoUrl == "" {
		return nil, fmt.Errorf("userinfoUrl is required")
	}
	if spec.ClientId == "" {
		return nil, fmt.Errorf("clientId is required")
	}
	if spec.ClientSecret == nil || *spec.ClientSecret == "" {
		return nil, fmt.Errorf("clientSecret is required")
	}

	// Apply defaults
	if spec.UsernameClaim == nil || len(*spec.UsernameClaim) == 0 {
		defaultUsernameClaim := []string{"preferred_username"}
		spec.UsernameClaim = &defaultUsernameClaim
	}

	// Use authorizationUrl as issuer if issuer is not provided
	if spec.Issuer == nil || *spec.Issuer == "" {
		spec.Issuer = &spec.AuthorizationUrl
	}

	// Convert organization assignment to org config
	orgConfig := convertOrganizationAssignmentToOrgConfig(spec.OrganizationAssignment)

	// Create role extractor from role assignment
	roleExtractor := NewRoleExtractor(spec.RoleAssignment)

	// Create stateless organization extractor
	organizationExtractor := NewOrganizationExtractor(orgConfig)

	return &OAuth2Auth{
		metadata:              metadata,
		spec:                  spec,
		tlsConfig:             tlsConfig,
		orgConfig:             orgConfig,
		roleExtractor:         roleExtractor,
		log:                   log,
		organizationExtractor: organizationExtractor,
	}, nil
}

func (o *OAuth2Auth) IsEnabled() bool {
	return o.spec.Enabled != nil && *o.spec.Enabled
}

// GetOAuth2Spec returns the internal OAuth2 spec with client secret intact (for internal use only)
func (o *OAuth2Auth) GetOAuth2Spec() api.OAuth2ProviderSpec {
	return o.spec
}

// GetAuthToken extracts the OAuth2 access token from the HTTP request
func (o *OAuth2Auth) GetAuthToken(r *http.Request) (string, error) {
	// Extract Bearer token from Authorization header
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return "", fmt.Errorf("missing Authorization header")
	}

	// Check if it's a Bearer token
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return "", fmt.Errorf("invalid Authorization header format")
	}

	token := strings.TrimPrefix(authHeader, "Bearer ")
	if token == "" {
		return "", fmt.Errorf("empty Bearer token")
	}

	return token, nil
}

// GetAuthConfig returns the OAuth2 authentication configuration
func (o *OAuth2Auth) GetAuthConfig() *api.AuthConfig {
	orgEnabled := false
	if o.orgConfig != nil {
		orgEnabled = o.orgConfig.Enabled
	}

	provider := api.AuthProvider{
		ApiVersion: api.AuthProviderAPIVersion,
		Kind:       api.AuthProviderKind,
		Metadata:   o.metadata,
		Spec:       api.AuthProviderSpec{},
	}

	_ = provider.Spec.FromOAuth2ProviderSpec(o.spec)

	return &api.AuthConfig{
		ApiVersion:           api.AuthConfigAPIVersion,
		DefaultProvider:      o.metadata.Name,
		OrganizationsEnabled: &orgEnabled,
		Providers:            &[]api.AuthProvider{provider},
	}
}

// ValidateToken validates an OAuth2 access token by calling the userinfo endpoint
func (o *OAuth2Auth) ValidateToken(ctx context.Context, token string) error {
	// Call userinfo endpoint to validate token
	_, err := o.callUserinfoEndpoint(ctx, token)
	if err != nil {
		o.log.Debugf("OAuth2 token validation failed: %v", err)
		return fmt.Errorf("invalid token: %w", err)
	}
	return nil
}

// GetIdentity extracts user identity from the OAuth2 userinfo endpoint
func (o *OAuth2Auth) GetIdentity(ctx context.Context, token string) (common.Identity, error) {
	// Call userinfo endpoint to get user information
	userInfo, err := o.callUserinfoEndpoint(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("failed to get user info: %w", err)
	}

	// Extract username from the specified claim
	usernameClaim := *o.spec.UsernameClaim
	username := o.extractClaimByPath(userInfo, usernameClaim)
	if username == "" {
		return nil, fmt.Errorf("failed to extract username from claim path %v", usernameClaim)
	}

	// Extract org-scoped roles using the role extractor
	orgRoles := o.roleExtractor.ExtractOrgRolesFromMap(userInfo)

	// Extract organizations using stateless organization extractor with userinfo map
	organizations := o.organizationExtractor.ExtractOrganizations(userInfo, username)

	// Build ReportedOrganization with roles embedded
	reportedOrganizations, isSuperAdmin := common.BuildReportedOrganizations(organizations, orgRoles, false)

	// Create OAuth2 identity
	oauth2Identity := common.NewBaseIdentityWithIssuer(username, username, reportedOrganizations, identity.NewIssuer(identity.AuthTypeOAuth2, *o.spec.Issuer))
	oauth2Identity.SetSuperAdmin(isSuperAdmin)

	return oauth2Identity, nil
}

// callUserinfoEndpoint calls the OAuth2 userinfo endpoint with the access token
func (o *OAuth2Auth) callUserinfoEndpoint(ctx context.Context, token string) (map[string]interface{}, error) {
	// Create HTTP client with TLS config
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: o.tlsConfig,
		},
		Timeout: 30 * time.Second,
	}

	// Create request to userinfo endpoint
	req, err := http.NewRequestWithContext(ctx, "GET", o.spec.UserinfoUrl, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create userinfo request: %w", err)
	}

	// Add Authorization header with Bearer token
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	// Make the request
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call userinfo endpoint: %w", err)
	}
	defer resp.Body.Close()

	// Check response status
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("userinfo endpoint returned status %d", resp.StatusCode)
	}

	// Parse JSON response
	var userInfo map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&userInfo); err != nil {
		return nil, fmt.Errorf("failed to parse userinfo response: %w", err)
	}

	return userInfo, nil
}

// extractClaimByPath extracts a string value from claims using an array path
func (o *OAuth2Auth) extractClaimByPath(claims map[string]interface{}, path []string) string {
	if len(path) == 0 {
		return ""
	}

	current := claims

	for i, part := range path {
		if i == len(path)-1 {
			// Last part - extract the value
			if value, exists := current[part]; exists {
				if str, ok := value.(string); ok {
					return str
				}
			}
			return ""
		}

		// Navigate deeper into the object
		if next, exists := current[part]; exists {
			if nextMap, ok := next.(map[string]interface{}); ok {
				current = nextMap
			} else {
				return ""
			}
		} else {
			return ""
		}
	}

	return ""
}
