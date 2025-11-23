package authn

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/jellydator/ttlcache/v3"
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
	httpClient            *http.Client
	identityCache         *ttlcache.Cache[string, common.Identity]
	cancel                context.CancelFunc
	mu                    sync.Mutex
	started               bool
	stopOnce              sync.Once
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
	roleExtractor := NewRoleExtractor(spec.RoleAssignment, log)

	// Create stateless organization extractor
	organizationExtractor := NewOrganizationExtractor(orgConfig)

	// Create a reusable HTTP client with proper configuration
	// This avoids creating a new client on every request and properly manages connection pooling
	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig:       tlsConfig,
			DisableKeepAlives:     false,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
		Timeout: 30 * time.Second,
	}

	// Create identity cache with 10-minute TTL
	// This caches validated identities to avoid repeated userinfo endpoint calls
	identityCache := ttlcache.New[string, common.Identity](
		ttlcache.WithTTL[string, common.Identity](10 * time.Minute),
	)

	return &OAuth2Auth{
		metadata:              metadata,
		spec:                  spec,
		tlsConfig:             tlsConfig,
		orgConfig:             orgConfig,
		roleExtractor:         roleExtractor,
		log:                   log,
		organizationExtractor: organizationExtractor,
		httpClient:            httpClient,
		identityCache:         identityCache,
	}, nil
}

func (o *OAuth2Auth) IsEnabled() bool {
	return o.spec.Enabled != nil && *o.spec.Enabled
}

// Start starts the identity cache background cleanup
// Creates a child context that can be independently canceled via Stop()
func (o *OAuth2Auth) Start(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.started {
		return fmt.Errorf("OAuth2Auth provider already started")
	}

	// Create a child context so this provider can be stopped independently
	providerCtx, cancel := context.WithCancel(ctx)
	o.cancel = cancel

	// Start cache in a goroutine (cache.Start() blocks waiting for cleanup events)
	go o.identityCache.Start()

	go func() {
		<-providerCtx.Done()
		o.identityCache.Stop()
		o.log.Debugf("OAuth2Auth identity cache stopped")
	}()

	o.log.Debugf("OAuth2Auth identity cache started")
	o.started = true
	return nil
}

// Stop stops the identity cache and cancels the provider's context
func (o *OAuth2Auth) Stop() {
	o.mu.Lock()
	defer o.mu.Unlock()

	// Only stop if we were started
	if !o.started {
		return
	}

	o.stopOnce.Do(func() {
		if o.cancel != nil {
			o.log.Debugf("Stopping OAuth2Auth provider")
			o.cancel()
		}
	})
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
	orgEnabled := true // Organizations are always enabled

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
	// Check cache first
	cacheKey := o.tokenCacheKey(token)
	if item := o.identityCache.Get(cacheKey); item != nil {
		o.log.Debugf("OAuth2 token validation succeeded from cache")
		return nil
	}

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
	// Check cache first
	cacheKey := o.tokenCacheKey(token)
	if item := o.identityCache.Get(cacheKey); item != nil {
		o.log.Debugf("OAuth2 identity retrieved from cache")
		return item.Value(), nil
	}

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

	// Cache the identity
	o.identityCache.Set(cacheKey, oauth2Identity, ttlcache.DefaultTTL)
	o.log.Debugf("OAuth2 identity cached for user: %s", username)

	return oauth2Identity, nil
}

// callUserinfoEndpoint calls the OAuth2 userinfo endpoint with the access token
func (o *OAuth2Auth) callUserinfoEndpoint(ctx context.Context, token string) (map[string]interface{}, error) {
	o.log.Debugf("Starting userinfo endpoint call to %s", o.spec.UserinfoUrl)

	// Check if context is already canceled before making the request
	if ctx.Err() != nil {
		o.log.Warnf("Context already canceled before userinfo call: %v", ctx.Err())
		return nil, fmt.Errorf("context canceled before userinfo call: %w", ctx.Err())
	}

	// Create request to userinfo endpoint using the request context
	req, err := http.NewRequestWithContext(ctx, "GET", o.spec.UserinfoUrl, nil)
	if err != nil {
		o.log.Errorf("Failed to create userinfo request: %v", err)
		return nil, fmt.Errorf("failed to create userinfo request: %w", err)
	}

	// Add Authorization header with Bearer token
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	o.log.Debugf("Making userinfo request, context error state: %v", ctx.Err())

	// Make the request using the reusable HTTP client
	startTime := time.Now()
	resp, err := o.httpClient.Do(req)
	duration := time.Since(startTime)

	if err != nil {
		o.log.Errorf("Userinfo endpoint call failed after %v: %v, context error: %v", duration, err, ctx.Err())
		return nil, fmt.Errorf("failed to call userinfo endpoint: %w", err)
	}
	defer resp.Body.Close()

	o.log.Debugf("Userinfo endpoint responded with status: %d (took %v)", resp.StatusCode, duration)

	// Check response status
	if resp.StatusCode != http.StatusOK {
		o.log.Errorf("Userinfo endpoint returned non-OK status: %d", resp.StatusCode)
		return nil, fmt.Errorf("userinfo endpoint returned status %d", resp.StatusCode)
	}

	// Parse JSON response
	var userInfo map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&userInfo); err != nil {
		o.log.Errorf("Failed to parse userinfo response: %v", err)
		return nil, fmt.Errorf("failed to parse userinfo response: %w", err)
	}

	o.log.Debugf("Successfully retrieved userinfo for token (total time: %v)", time.Since(startTime))
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

// tokenCacheKey creates a SHA256 hash of the token for use as a cache key
// We hash the token to avoid storing sensitive token data in memory as cache keys
func (o *OAuth2Auth) tokenCacheKey(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}
