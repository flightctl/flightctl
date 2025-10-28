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
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

// OAuth2Auth implements OAuth2 authentication using userinfo endpoint validation
type OAuth2Auth struct {
	providerName          string
	issuer                string
	authorizationUrl      string
	tokenUrl              string
	userinfoUrl           string
	clientId              string
	clientSecret          string
	scopes                []string
	tlsConfig             *tls.Config
	orgConfig             *common.AuthOrganizationsConfig
	usernameClaim         string
	roleClaim             string
	log                   logrus.FieldLogger
	organizationExtractor *OrganizationExtractor
}

// NewOAuth2Auth creates a new OAuth2 authentication instance
func NewOAuth2Auth(
	providerName string,
	issuer, authorizationUrl, tokenUrl, userinfoUrl, clientId, clientSecret string,
	scopes []string,
	tlsConfig *tls.Config,
	orgConfig *common.AuthOrganizationsConfig,
	usernameClaim, roleClaim string,
	log logrus.FieldLogger,
) (*OAuth2Auth, error) {
	if issuer == "" {
		return nil, fmt.Errorf("issuer is required")
	}
	if authorizationUrl == "" {
		return nil, fmt.Errorf("authorizationUrl is required")
	}
	if tokenUrl == "" {
		return nil, fmt.Errorf("tokenUrl is required")
	}
	if userinfoUrl == "" {
		return nil, fmt.Errorf("userinfoUrl is required")
	}
	if clientId == "" {
		return nil, fmt.Errorf("clientId is required")
	}
	if clientSecret == "" {
		return nil, fmt.Errorf("clientSecret is required")
	}

	if usernameClaim == "" {
		usernameClaim = "preferred_username"
	}
	if roleClaim == "" {
		roleClaim = "groups"
	}

	// Create stateless organization extractor
	organizationExtractor := NewOrganizationExtractor(orgConfig)

	return &OAuth2Auth{
		providerName:          providerName,
		issuer:                issuer,
		authorizationUrl:      authorizationUrl,
		tokenUrl:              tokenUrl,
		userinfoUrl:           userinfoUrl,
		clientId:              clientId,
		clientSecret:          clientSecret,
		scopes:                scopes,
		tlsConfig:             tlsConfig,
		orgConfig:             orgConfig,
		usernameClaim:         usernameClaim,
		roleClaim:             roleClaim,
		log:                   log,
		organizationExtractor: organizationExtractor,
	}, nil
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

	providerType := string(api.AuthProviderInfoTypeOauth2)
	provider := api.AuthProviderInfo{
		Name:          &o.providerName,
		Type:          (*api.AuthProviderInfoType)(&providerType),
		Issuer:        &o.issuer,
		AuthUrl:       &o.authorizationUrl,
		TokenUrl:      &o.tokenUrl,
		UserinfoUrl:   &o.userinfoUrl,
		ClientId:      &o.clientId,
		Scopes:        &o.scopes,
		UsernameClaim: &o.usernameClaim,
		IsDefault:     lo.ToPtr(true),
		IsStatic:      lo.ToPtr(true),
	}

	return &api.AuthConfig{
		DefaultProvider:      &providerType,
		OrganizationsEnabled: &orgEnabled,
		Providers:            &[]api.AuthProviderInfo{provider},
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
	username, err := o.extractClaim(userInfo, o.usernameClaim)
	if err != nil {
		return nil, fmt.Errorf("failed to extract username from claim %s: %w", o.usernameClaim, err)
	}

	// Extract roles from the specified claim
	roles, err := o.extractRoles(userInfo, o.roleClaim)
	if err != nil {
		o.log.Warnf("Failed to extract roles from claim %s: %v", o.roleClaim, err)
		roles = []string{} // Continue without roles
	}

	// Extract organizations using stateless organization extractor with userinfo map
	organizations := o.organizationExtractor.ExtractOrganizations(userInfo, username)
	reportedOrganizations := make([]common.ReportedOrganization, 0, len(organizations))
	for _, org := range organizations {
		reportedOrganizations = append(reportedOrganizations, common.ReportedOrganization{
			Name:         org,
			IsInternalID: false,
			ID:           org,
		})
	}
	// Create OAuth2 identity
	oauth2Identity := common.NewBaseIdentityWithIssuer(username, username, reportedOrganizations, roles, identity.NewIssuer(identity.AuthTypeOAuth2, o.issuer))

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
	req, err := http.NewRequestWithContext(ctx, "GET", o.userinfoUrl, nil)
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

// extractClaim extracts a claim value from the userinfo response
func (o *OAuth2Auth) extractClaim(userInfo map[string]interface{}, claimPath string) (string, error) {
	// Handle simple claim paths (e.g., "preferred_username", "email")
	if value, exists := userInfo[claimPath]; exists {
		if str, ok := value.(string); ok {
			return str, nil
		}
		return "", fmt.Errorf("claim %s is not a string", claimPath)
	}

	// Handle nested claim paths (e.g., "realm_access.roles")
	parts := strings.Split(claimPath, ".")
	current := userInfo

	for i, part := range parts {
		if i == len(parts)-1 {
			// Last part - extract the value
			if value, exists := current[part]; exists {
				if str, ok := value.(string); ok {
					return str, nil
				}
				return "", fmt.Errorf("claim %s is not a string", claimPath)
			}
			return "", fmt.Errorf("claim %s not found", claimPath)
		}

		// Navigate deeper into the object
		if next, exists := current[part]; exists {
			if nextMap, ok := next.(map[string]interface{}); ok {
				current = nextMap
			} else {
				return "", fmt.Errorf("claim path %s is not an object", strings.Join(parts[:i+1], "."))
			}
		} else {
			return "", fmt.Errorf("claim path %s not found", strings.Join(parts[:i+1], "."))
		}
	}

	return "", fmt.Errorf("claim %s not found", claimPath)
}

// extractRoles extracts roles from the userinfo response
func (o *OAuth2Auth) extractRoles(userInfo map[string]interface{}, roleClaim string) ([]string, error) {
	// Handle simple role claim (e.g., "groups")
	if value, exists := userInfo[roleClaim]; exists {
		if roles, ok := value.([]interface{}); ok {
			var result []string
			for _, role := range roles {
				if str, ok := role.(string); ok {
					result = append(result, str)
				}
			}
			return result, nil
		}
		return nil, fmt.Errorf("role claim %s is not an array", roleClaim)
	}

	// Handle nested role claim (e.g., "realm_access.roles")
	parts := strings.Split(roleClaim, ".")
	current := userInfo

	for i, part := range parts {
		if i == len(parts)-1 {
			// Last part - extract the roles array
			if value, exists := current[part]; exists {
				if roles, ok := value.([]interface{}); ok {
					var result []string
					for _, role := range roles {
						if str, ok := role.(string); ok {
							result = append(result, str)
						}
					}
					return result, nil
				}
				return nil, fmt.Errorf("role claim %s is not an array", roleClaim)
			}
			return nil, fmt.Errorf("role claim %s not found", roleClaim)
		}

		// Navigate deeper into the object
		if next, exists := current[part]; exists {
			if nextMap, ok := next.(map[string]interface{}); ok {
				current = nextMap
			} else {
				return nil, fmt.Errorf("role claim path %s is not an object", strings.Join(parts[:i+1], "."))
			}
		} else {
			return nil, fmt.Errorf("role claim path %s not found", strings.Join(parts[:i+1], "."))
		}
	}

	return nil, fmt.Errorf("role claim %s not found", roleClaim)
}
