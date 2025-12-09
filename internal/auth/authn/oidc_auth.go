package authn

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/auth/common"
	identitypkg "github.com/flightctl/flightctl/internal/identity"
	"github.com/jellydator/ttlcache/v3"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
	"github.com/sirupsen/logrus"
)

const (
	subClaim = "sub"
	// defaultOIDCTimeout is the timeout for OIDC discovery and JWKS refresh requests
	defaultOIDCTimeout = 10 * time.Second
)

type TokenIdentity interface {
	common.Identity
	GetClaim(string) (interface{}, bool)
}

// JWTIdentity extends common.Identity with JWT-specific fields
type JWTIdentity struct {
	common.BaseIdentity
	parsedToken jwt.Token
}

// Ensure JWTIdentity implements TokenIdentity
var _ TokenIdentity = (*JWTIdentity)(nil)

func (i *JWTIdentity) GetClaim(claim string) (interface{}, bool) {
	return i.parsedToken.Get(claim)
}

type OIDCAuth struct {
	metadata              api.ObjectMeta
	spec                  api.OIDCProviderSpec
	jwksUri               string
	clientTlsConfig       *tls.Config
	client                *http.Client
	orgConfig             *common.AuthOrganizationsConfig
	roleExtractor         *RoleExtractor
	jwksCache             *jwk.Cache
	organizationExtractor *OrganizationExtractor
	log                   logrus.FieldLogger
	identityCache         *ttlcache.Cache[string, common.Identity]

	// Lazy OIDC discovery initialization
	// We need to fetch the discovery document once to get the JWKS URL,
	// then Register() it with the cache. After that, cache.Get() handles everything.
	discoveryOnce sync.Once
	discoveryErr  error

	// Lifecycle management
	cancel   context.CancelFunc
	mu       sync.Mutex
	started  bool
	stopOnce sync.Once
}

type OIDCServerResponse struct {
	TokenEndpoint string `json:"token_endpoint"`
	JwksUri       string `json:"jwks_uri"`
}

func NewOIDCAuth(metadata api.ObjectMeta, spec api.OIDCProviderSpec, clientTlsConfig *tls.Config, log logrus.FieldLogger) (*OIDCAuth, error) {
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

	// Create identity cache with 10-minute TTL
	// This caches validated identities to avoid repeated JWT validation
	identityCache := ttlcache.New[string, common.Identity](
		ttlcache.WithTTL[string, common.Identity](10 * time.Minute),
	)

	oidcAuth := &OIDCAuth{
		metadata:        metadata,
		spec:            spec,
		clientTlsConfig: clientTlsConfig,
		client: &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: clientTlsConfig,
			},
			Timeout: defaultOIDCTimeout,
		},
		orgConfig:     orgConfig,
		roleExtractor: roleExtractor,
		log:           log,
		identityCache: identityCache,
	}

	// Create stateless organization extractor
	oidcAuth.organizationExtractor = NewOrganizationExtractor(orgConfig)

	// Note: OIDC discovery (.well-known/openid-configuration) is fetched lazily on first token validation
	// This prevents startup deadlocks when the API server is its own OIDC provider

	return oidcAuth, nil
}

func (o *OIDCAuth) IsEnabled() bool {
	return o.spec.Enabled != nil && *o.spec.Enabled
}

// Start starts the identity cache background cleanup
// Creates a child context that can be independently canceled via Stop()
func (o *OIDCAuth) Start(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.started {
		return fmt.Errorf("OIDCAuth provider already started")
	}

	// Create a child context so this provider can be stopped independently
	providerCtx, cancel := context.WithCancel(ctx)
	o.cancel = cancel

	// Start cache in a goroutine (cache.Start() blocks waiting for cleanup events)
	go o.identityCache.Start()

	go func() {
		<-providerCtx.Done()
		o.identityCache.Stop()
		o.log.Debugf("OIDCAuth identity cache stopped")
	}()

	o.log.Debugf("OIDCAuth identity cache started")
	o.started = true
	return nil
}

// Stop stops the identity cache and cancels the provider's context
func (o *OIDCAuth) Stop() {
	o.mu.Lock()
	defer o.mu.Unlock()

	// Only stop if we were started
	if !o.started {
		return
	}

	o.stopOnce.Do(func() {
		if o.cancel != nil {
			o.log.Debugf("Stopping OIDCAuth provider")
			o.cancel()
		}
	})
}

// ensureDiscovery performs lazy OIDC discovery on first use
// This is called automatically before validating tokens
func (o *OIDCAuth) ensureDiscovery(ctx context.Context) error {
	o.discoveryOnce.Do(func() {
		discoveryURL := fmt.Sprintf("%s/.well-known/openid-configuration", o.spec.Issuer)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
		if err != nil {
			o.discoveryErr = fmt.Errorf("failed to create OIDC discovery request: %w", err)
			return
		}

		res, err := o.client.Do(req)
		if err != nil {
			o.discoveryErr = fmt.Errorf("failed to fetch OIDC discovery document: %w", err)
			return
		}
		defer res.Body.Close()

		if res.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(res.Body)
			o.discoveryErr = fmt.Errorf("OIDC discovery request failed with status %d: %s", res.StatusCode, string(bodyBytes))
			return
		}

		oidcResponse := OIDCServerResponse{}
		bodyBytes, err := io.ReadAll(res.Body)
		if err != nil {
			o.discoveryErr = fmt.Errorf("failed to read OIDC discovery response: %w", err)
			return
		}

		if err := json.Unmarshal(bodyBytes, &oidcResponse); err != nil {
			o.discoveryErr = fmt.Errorf("failed to parse OIDC discovery document: %w", err)
			return
		}

		o.jwksUri = oidcResponse.JwksUri

		// Initialize JWKS cache with 15-minute refresh interval
		// This balances performance with key rotation requirements
		o.jwksCache = jwk.NewCache(ctx)
		if err := o.jwksCache.Register(o.jwksUri, jwk.WithMinRefreshInterval(15*time.Minute), jwk.WithHTTPClient(o.client)); err != nil {
			o.discoveryErr = fmt.Errorf("failed to register JWKS cache: %w", err)
			return
		}
	})

	return o.discoveryErr
}

func (o *OIDCAuth) ValidateToken(ctx context.Context, token string) error {
	_, err := o.parseAndCreateIdentity(ctx, token)
	return err
}

func (o *OIDCAuth) parseAndCreateIdentity(ctx context.Context, token string) (*JWTIdentity, error) {
	startTime := time.Now()
	o.log.Debugf("OIDC: Starting token validation")

	// Step 1: Quick parse WITHOUT signature verification to fast-fail on non-JWT tokens
	// This avoids expensive OIDC discovery and JWKS fetching for non-JWT tokens
	parseStart := time.Now()
	_, err := jwt.Parse([]byte(token), jwt.WithValidate(false), jwt.WithVerify(false))
	if err != nil {
		o.log.Debugf("OIDC: Token is not JWT format (took %v): %v", time.Since(parseStart), err)
		return nil, fmt.Errorf("failed to parse JWT token: %w", err)
	}
	o.log.Debugf("OIDC: Token structure validated (took %v)", time.Since(parseStart))

	// Step 2: Ensure OIDC discovery has been performed (lazy initialization)
	discoveryStart := time.Now()
	if err := o.ensureDiscovery(ctx); err != nil {
		o.log.Errorf("OIDC: Discovery failed after %v: %v", time.Since(discoveryStart), err)
		return nil, fmt.Errorf("OIDC discovery failed: %w", err)
	}
	o.log.Debugf("OIDC: Discovery completed (took %v)", time.Since(discoveryStart))

	// Step 3: Get JWK set from cache
	jwksGetStart := time.Now()
	jwkSet, err := o.jwksCache.Get(ctx, o.jwksUri)
	if err != nil {
		o.log.Errorf("OIDC: Failed to get JWKS from cache after %v: %v", time.Since(jwksGetStart), err)
		return nil, fmt.Errorf("failed to get JWK set from cache: %w", err)
	}
	o.log.Debugf("OIDC: JWKS retrieved from cache (took %v)", time.Since(jwksGetStart))

	// Step 4: Parse and validate token WITH signature verification using JWKS
	// This is the second parse, but now we know it's a valid JWT structure
	validateStart := time.Now()
	parsedToken, err := jwt.Parse([]byte(token), jwt.WithKeySet(jwkSet), jwt.WithValidate(true))
	validateDuration := time.Since(validateStart)

	if err != nil {
		o.log.Debugf("OIDC: JWT signature validation failed after %v: %v", validateDuration, err)
		// If token validation fails, it might be due to key rotation
		// Try to refresh the cache and retry once
		if o.jwksCache != nil {
			refreshStart := time.Now()
			if _, refreshErr := o.jwksCache.Refresh(ctx, o.jwksUri); refreshErr == nil {
				o.log.Debugf("OIDC: JWKS cache refreshed (took %v)", time.Since(refreshStart))
				// Retry with refreshed keys
				jwkSet, retryErr := o.jwksCache.Get(ctx, o.jwksUri)
				if retryErr == nil {
					retryValidateStart := time.Now()
					parsedToken, retryErr = jwt.Parse([]byte(token), jwt.WithKeySet(jwkSet), jwt.WithValidate(true))
					if retryErr == nil {
						o.log.Debugf("OIDC: JWT signature validation succeeded on retry (took %v)", time.Since(retryValidateStart))
						err = nil // Clear the original error
					} else {
						o.log.Debugf("OIDC: JWT signature validation failed on retry after %v: %v", time.Since(retryValidateStart), retryErr)
					}
				}
			} else {
				o.log.Debugf("OIDC: JWKS cache refresh failed after %v: %v", time.Since(refreshStart), refreshErr)
			}
		}

		if err != nil {
			o.log.Errorf("OIDC: Token validation failed (total time: %v)", time.Since(startTime))
			return nil, fmt.Errorf("failed to validate JWT token: %w", err)
		}
	} else {
		o.log.Debugf("OIDC: JWT signature validation succeeded (took %v)", validateDuration)
	}

	o.log.Debugf("OIDC: Token fully validated (total time: %v)", time.Since(startTime))

	// Validate audience claim contains expected client ID
	if o.spec.ClientId != "" {
		audienceValid := false
		for _, v := range parsedToken.Audience() {
			if v == o.spec.ClientId {
				audienceValid = true
				break
			}
		}

		if !audienceValid {
			return nil, fmt.Errorf("token audience does not contain expected client ID '%s'", o.spec.ClientId)
		}

		// Also validate azp claim if present
		if azp, exists := parsedToken.Get("azp"); exists {
			if azpStr, ok := azp.(string); ok {
				if azpStr != o.spec.ClientId {
					return nil, fmt.Errorf("token authorized party '%s' does not match expected client ID '%s'", azpStr, o.spec.ClientId)
				}
			}
		}
	}

	identity := &JWTIdentity{}
	identity.parsedToken = parsedToken

	if sub, exists := parsedToken.Get(subClaim); exists {
		if uid, ok := sub.(string); ok {
			identity.SetUID(uid)
		}
	}

	// Convert JWT token to claims map for username extraction
	claimsMap, err := parsedToken.AsMap(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to convert JWT token to map: %w", err)
	}

	// Extract username using the claim path
	username := ""
	if o.spec.UsernameClaim != nil && len(*o.spec.UsernameClaim) > 0 {
		if usernameValue := o.extractClaimByPath(claimsMap, *o.spec.UsernameClaim); usernameValue != "" {
			username = usernameValue
			identity.SetUsername(usernameValue)
		}
	}

	// Extract org-scoped roles using the role extractor
	orgRoles := o.roleExtractor.ExtractOrgRolesFromMap(claimsMap)

	// Use the stateless organization extractor with claims map
	organizations := o.organizationExtractor.ExtractOrganizations(claimsMap, username)

	// Build ReportedOrganization with roles embedded
	reportedOrganizations, isSuperAdmin := common.BuildReportedOrganizations(organizations, orgRoles, false)
	identity.SetOrganizations(reportedOrganizations)
	identity.SetSuperAdmin(isSuperAdmin)

	// Set the issuer from JWT token
	if issuer := parsedToken.Issuer(); issuer != "" {
		issuer := identitypkg.NewIssuer(identitypkg.AuthTypeOIDC, issuer)
		identity.SetIssuer(issuer)
	}

	return identity, nil
}

func (o *OIDCAuth) GetIdentity(ctx context.Context, token string) (common.Identity, error) {
	// Check cache first
	cacheKey := o.tokenCacheKey(token)
	if item := o.identityCache.Get(cacheKey); item != nil {
		o.log.Debugf("OIDC identity retrieved from cache")
		return item.Value(), nil
	}

	identity, err := o.parseAndCreateIdentity(ctx, token)
	if err != nil {
		return nil, err
	}

	// Cache the identity
	o.identityCache.Set(cacheKey, identity, ttlcache.DefaultTTL)
	o.log.Debugf("OIDC identity cached for user: %s", identity.GetUsername())

	return identity, nil
}

// GetOIDCSpec returns the internal OIDC spec with client secret intact (for internal use only)
func (o *OIDCAuth) GetOIDCSpec() api.OIDCProviderSpec {
	return o.spec
}

func (o *OIDCAuth) GetAuthConfig() *api.AuthConfig {
	orgEnabled := true // Organizations are always enabled

	provider := api.AuthProvider{
		ApiVersion: api.AuthProviderAPIVersion,
		Kind:       api.AuthProviderKind,
		Metadata:   o.metadata,
		Spec:       api.AuthProviderSpec{},
	}

	// Create a copy of the spec - client secret will be masked during JSON marshaling
	maskedSpec := o.spec

	_ = provider.Spec.FromOIDCProviderSpec(maskedSpec)

	return &api.AuthConfig{
		ApiVersion:           api.AuthConfigAPIVersion,
		DefaultProvider:      o.metadata.Name,
		OrganizationsEnabled: &orgEnabled,
		Providers:            &[]api.AuthProvider{provider},
	}
}

func (o *OIDCAuth) GetAuthToken(r *http.Request) (string, error) {
	return common.ExtractBearerToken(r)
}

// tokenCacheKey creates a SHA256 hash of the token for use as a cache key
// We hash the token to avoid storing sensitive token data in memory as cache keys
func (o *OIDCAuth) tokenCacheKey(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

// extractClaimByPath extracts a string value from claims using an array path
func (o *OIDCAuth) extractClaimByPath(claims map[string]interface{}, path []string) string {
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
