package authn

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/jellydator/ttlcache/v3"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
	"github.com/samber/lo"
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
	jwksCache             *jwk.Cache
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

	// Convert organization assignment to org config
	orgConfig := convertOrganizationAssignmentToOrgConfig(spec.OrganizationAssignment)

	// Check if AuthProvider was created by super admin
	createdBySuperAdmin := false
	if metadata.Annotations != nil {
		if val, ok := (*metadata.Annotations)[api.AuthProviderAnnotationCreatedBySuperAdmin]; ok && val == "true" {
			createdBySuperAdmin = true
		}
	}

	// Create role extractor from role assignment with super admin flag
	roleExtractor := NewRoleExtractor(spec.RoleAssignment, createdBySuperAdmin, log)

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

	oauth2Auth := &OAuth2Auth{
		metadata:              metadata,
		spec:                  spec,
		tlsConfig:             tlsConfig,
		orgConfig:             orgConfig,
		roleExtractor:         roleExtractor,
		log:                   log,
		organizationExtractor: organizationExtractor,
		httpClient:            httpClient,
		identityCache:         identityCache,
	}

	// Initialize JWKS cache if using JWT introspection
	if spec.Introspection != nil {
		discriminator, err := spec.Introspection.Discriminator()
		if err == nil && discriminator == string(api.Jwt) {
			jwtSpec, err := spec.Introspection.AsJwtIntrospectionSpec()
			if err == nil {
				// Create JWKS cache - it will fetch on first Get() call
				jwksCache := jwk.NewCache(context.Background())
				if err := jwksCache.Register(jwtSpec.JwksUrl, jwk.WithMinRefreshInterval(15*time.Minute), jwk.WithHTTPClient(httpClient)); err != nil {
					return nil, fmt.Errorf("failed to register JWKS cache: %w", err)
				}
				oauth2Auth.jwksCache = jwksCache
				log.Debugf("OAuth2Auth JWKS cache registered for %s", jwtSpec.JwksUrl)
			}
		}
	}

	return oauth2Auth, nil
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

	// Start identity cache in a goroutine (cache.Start() blocks waiting for cleanup events)
	go o.identityCache.Start()

	go func() {
		<-providerCtx.Done()
		o.identityCache.Stop()
		o.log.Debugf("OAuth2Auth identity cache stopped")
	}()

	// Warm up JWKS cache if using JWT introspection
	if o.jwksCache != nil {
		if o.spec.Introspection != nil {
			jwtSpec, err := o.spec.Introspection.AsJwtIntrospectionSpec()
			if err == nil {
				// Fetch JWKS in background to warm up cache
				go func() {
					_, err := o.jwksCache.Get(providerCtx, jwtSpec.JwksUrl)
					if err != nil {
						o.log.Warnf("Failed to warm up JWKS cache: %v", err)
					} else {
						o.log.Debugf("JWKS cache warmed up for %s", jwtSpec.JwksUrl)
					}
				}()
			}
		}
	}

	o.log.Debugf("OAuth2Auth started")
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

// ValidateToken validates an OAuth2 access token using the configured introspection method
func (o *OAuth2Auth) ValidateToken(ctx context.Context, token string) error {
	// Check cache first
	cacheKey := o.tokenCacheKey(token)
	if item := o.identityCache.Get(cacheKey); item != nil {
		o.log.Debugf("OAuth2 token validation succeeded from cache")
		return nil
	}

	// Use introspection if configured, otherwise fall back to userinfo endpoint
	if o.spec.Introspection != nil {
		err := o.introspectToken(ctx, token)
		if err != nil {
			o.log.Debugf("OAuth2 token introspection failed: %v", err)
			return fmt.Errorf("invalid token: %w", err)
		}
		return nil
	}

	// Fall back to userinfo endpoint for validation
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
	// UsernameClaim should be set by validation, but handle nil case gracefully
	if o.spec.UsernameClaim == nil || len(*o.spec.UsernameClaim) == 0 {
		return nil, fmt.Errorf("usernameClaim is required but not set")
	}
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

	// Get issuer (should be set by validation, but fallback to AuthorizationUrl if not)
	issuer := lo.FromPtr(o.spec.Issuer)
	if issuer == "" {
		issuer = o.spec.AuthorizationUrl
	}

	// Create OAuth2 identity
	oauth2Identity := common.NewBaseIdentityWithIssuer(username, username, reportedOrganizations, identity.NewIssuer(identity.AuthTypeOAuth2, issuer))
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

// introspectToken validates a token using the configured introspection method
func (o *OAuth2Auth) introspectToken(ctx context.Context, token string) error {
	discriminator, err := o.spec.Introspection.Discriminator()
	if err != nil {
		return fmt.Errorf("failed to determine introspection type: %w", err)
	}

	switch discriminator {
	case string(api.Rfc7662):
		spec, err := o.spec.Introspection.AsRfc7662IntrospectionSpec()
		if err != nil {
			return fmt.Errorf("failed to parse RFC7662 introspection spec: %w", err)
		}
		return o.introspectRFC7662(ctx, token, spec)
	case string(api.Github):
		spec, err := o.spec.Introspection.AsGitHubIntrospectionSpec()
		if err != nil {
			return fmt.Errorf("failed to parse GitHub introspection spec: %w", err)
		}
		return o.introspectGitHub(ctx, token, spec)
	case string(api.Jwt):
		spec, err := o.spec.Introspection.AsJwtIntrospectionSpec()
		if err != nil {
			return fmt.Errorf("failed to parse JWT introspection spec: %w", err)
		}
		return o.introspectJWT(ctx, token, spec)
	default:
		return fmt.Errorf("unsupported introspection type: %s", discriminator)
	}
}

// introspectRFC7662 validates a token using RFC 7662 token introspection
func (o *OAuth2Auth) introspectRFC7662(ctx context.Context, token string, spec api.Rfc7662IntrospectionSpec) error {
	o.log.Debugf("Starting RFC 7662 introspection to %s", spec.Url)

	// Create form data for introspection request with proper URL encoding
	formData := url.Values{}
	formData.Set("token", token)

	req, err := http.NewRequestWithContext(ctx, "POST", spec.Url, strings.NewReader(formData.Encode()))
	if err != nil {
		return fmt.Errorf("failed to create introspection request: %w", err)
	}

	// RFC 7662 requires client authentication via Basic Auth
	req.SetBasicAuth(o.spec.ClientId, *o.spec.ClientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to call introspection endpoint: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("introspection endpoint returned status %d", resp.StatusCode)
	}

	// Parse introspection response
	var introspectionResponse struct {
		Active bool `json:"active"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&introspectionResponse); err != nil {
		return fmt.Errorf("failed to parse introspection response: %w", err)
	}

	if !introspectionResponse.Active {
		return fmt.Errorf("token is not active")
	}

	o.log.Debugf("RFC 7662 introspection succeeded")
	return nil
}

// introspectGitHub validates a token using GitHub's application token API
func (o *OAuth2Auth) introspectGitHub(ctx context.Context, token string, spec api.GitHubIntrospectionSpec) error {
	// GitHub uses: POST /applications/{client_id}/token
	// Default to https://api.github.com if no URL specified
	baseURL := "https://api.github.com"
	if spec.Url != nil && *spec.Url != "" {
		baseURL = *spec.Url
	}

	url := fmt.Sprintf("%s/applications/%s/token", baseURL, o.spec.ClientId)
	o.log.Debugf("Starting GitHub introspection to %s", url)

	requestBody := map[string]string{"access_token": token}
	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("failed to marshal GitHub introspection request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("failed to create GitHub introspection request: %w", err)
	}

	// GitHub requires Basic Auth with client ID and secret
	req.SetBasicAuth(o.spec.ClientId, *o.spec.ClientSecret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to call GitHub introspection endpoint: %w", err)
	}
	defer resp.Body.Close()

	// GitHub returns 200 for valid tokens, 404 for invalid ones
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("token is invalid or not owned by this application")
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub introspection endpoint returned status %d", resp.StatusCode)
	}

	o.log.Debugf("GitHub introspection succeeded")
	return nil
}

// introspectJWT validates a token as a JWT using JWKS
func (o *OAuth2Auth) introspectJWT(ctx context.Context, token string, spec api.JwtIntrospectionSpec) error {
	o.log.Debugf("Starting JWT introspection with JWKS URL: %s", spec.JwksUrl)

	// Parse JWT without validation first to fast-fail on non-JWT tokens
	_, err := jwt.Parse([]byte(token), jwt.WithValidate(false), jwt.WithVerify(false))
	if err != nil {
		o.log.Debugf("Token is not JWT format: %v", err)
		return fmt.Errorf("failed to parse JWT token: %w", err)
	}

	// Get JWKS from cache (will fetch if not cached)
	jwkSet, err := o.jwksCache.Get(ctx, spec.JwksUrl)
	if err != nil {
		o.log.Errorf("Failed to get JWKS from cache: %v", err)
		return fmt.Errorf("failed to get JWK set from cache: %w", err)
	}

	// Parse and validate token with signature verification
	// The jwksCache automatically refreshes based on WithMinRefreshInterval (15 minutes)
	parsedToken, err := jwt.Parse([]byte(token), jwt.WithKeySet(jwkSet), jwt.WithValidate(true))
	if err != nil {
		o.log.Debugf("JWT validation failed: %v", err)
		return fmt.Errorf("failed to validate JWT token: %w", err)
	}

	// Validate issuer if specified, otherwise use OAuth2ProviderSpec issuer
	expectedIssuer := ""
	if spec.Issuer != nil && *spec.Issuer != "" {
		expectedIssuer = *spec.Issuer
	} else if o.spec.Issuer != nil && *o.spec.Issuer != "" {
		expectedIssuer = *o.spec.Issuer
	}

	if expectedIssuer != "" && parsedToken.Issuer() != expectedIssuer {
		return fmt.Errorf("token issuer '%s' does not match expected issuer '%s'", parsedToken.Issuer(), expectedIssuer)
	}

	// Validate audience if specified, otherwise use OAuth2ProviderSpec clientId
	var expectedAudiences []string
	if spec.Audience != nil && len(*spec.Audience) > 0 {
		expectedAudiences = *spec.Audience
	} else if o.spec.ClientId != "" {
		expectedAudiences = []string{o.spec.ClientId}
	}

	if len(expectedAudiences) > 0 {
		audienceValid := false
		tokenAudiences := parsedToken.Audience()
		for _, expectedAud := range expectedAudiences {
			if slices.Contains(tokenAudiences, expectedAud) {
				audienceValid = true
				break
			}
		}

		if !audienceValid {
			return fmt.Errorf("token audience %v does not contain any of the expected audiences %v", tokenAudiences, expectedAudiences)
		}
	}

	o.log.Debugf("JWT introspection succeeded")
	return nil
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
