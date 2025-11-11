package authn

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/auth/common"
	identitypkg "github.com/flightctl/flightctl/internal/identity"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
	"github.com/samber/lo"
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
	providerName          string
	displayName           string
	oidcAuthority         string
	externalOidcAuthority string
	jwksUri               string
	clientTlsConfig       *tls.Config
	client                *http.Client
	orgConfig             *common.AuthOrganizationsConfig
	usernameClaim         []string
	roleExtractor         *RoleExtractor
	expectedClientId      string
	scopes                []string
	jwksCache             *jwk.Cache
	organizationExtractor *OrganizationExtractor

	// Lazy OIDC discovery initialization
	// We need to fetch the discovery document once to get the JWKS URL,
	// then Register() it with the cache. After that, cache.Get() handles everything.
	discoveryOnce sync.Once
	discoveryErr  error
}

type OIDCServerResponse struct {
	TokenEndpoint string `json:"token_endpoint"`
	JwksUri       string `json:"jwks_uri"`
}

func NewOIDCAuth(providerName string, displayName string, oidcAuthority string, externalOidcAuthority string, clientTlsConfig *tls.Config, orgConfig *common.AuthOrganizationsConfig, usernameClaim []string, roleExtractor *RoleExtractor, expectedClientId string, scopes []string) (*OIDCAuth, error) {
	// If external authority is not provided, use the internal authority
	if externalOidcAuthority == "" {
		externalOidcAuthority = oidcAuthority
	}

	oidcAuth := &OIDCAuth{
		providerName:          providerName,
		displayName:           displayName,
		oidcAuthority:         oidcAuthority,
		externalOidcAuthority: externalOidcAuthority,
		clientTlsConfig:       clientTlsConfig,
		client: &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: clientTlsConfig,
			},
			Timeout: defaultOIDCTimeout,
		},
		orgConfig:        orgConfig,
		usernameClaim:    usernameClaim,
		roleExtractor:    roleExtractor,
		expectedClientId: expectedClientId,
		scopes:           scopes,
	}

	// Create stateless organization extractor
	oidcAuth.organizationExtractor = NewOrganizationExtractor(orgConfig)

	// Note: OIDC discovery (.well-known/openid-configuration) is fetched lazily on first token validation
	// This prevents startup deadlocks when the API server is its own OIDC provider

	return oidcAuth, nil
}

// ensureDiscovery performs lazy OIDC discovery on first use
// This is called automatically before validating tokens
func (o *OIDCAuth) ensureDiscovery(ctx context.Context) error {
	o.discoveryOnce.Do(func() {
		discoveryURL := fmt.Sprintf("%s/.well-known/openid-configuration", o.oidcAuthority)
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
	// Ensure OIDC discovery has been performed (lazy initialization)
	if err := o.ensureDiscovery(ctx); err != nil {
		return nil, fmt.Errorf("OIDC discovery failed: %w", err)
	}

	var jwkSet jwk.Set
	var err error

	// Get JWK set from cache
	jwkSet, err = o.jwksCache.Get(ctx, o.jwksUri)
	if err != nil {
		return nil, fmt.Errorf("failed to get JWK set from cache: %w", err)
	}

	parsedToken, err := jwt.Parse([]byte(token), jwt.WithKeySet(jwkSet), jwt.WithValidate(true))
	if err != nil {
		// If token validation fails, it might be due to key rotation
		// Try to refresh the cache and retry once (only if cache is available)
		if o.jwksCache != nil {
			if _, refreshErr := o.jwksCache.Refresh(ctx, o.jwksUri); refreshErr == nil {
				// Retry with refreshed keys
				jwkSet, retryErr := o.jwksCache.Get(ctx, o.jwksUri)
				if retryErr == nil {
					parsedToken, retryErr = jwt.Parse([]byte(token), jwt.WithKeySet(jwkSet), jwt.WithValidate(true))
					if retryErr == nil {
						err = nil // Clear the original error
					}
				}
			}
		}

		if err != nil {
			return nil, fmt.Errorf("failed to parse JWT token: %w", err)
		}
	}

	// Validate audience claim contains expected client ID
	if o.expectedClientId != "" {
		audienceValid := false
		for _, v := range parsedToken.Audience() {
			if v == o.expectedClientId {
				audienceValid = true
				break
			}
		}

		if !audienceValid {
			return nil, fmt.Errorf("token audience does not contain expected client ID '%s'", o.expectedClientId)
		}

		// Also validate azp claim if present
		if azp, exists := parsedToken.Get("azp"); exists {
			if azpStr, ok := azp.(string); ok {
				if azpStr != o.expectedClientId {
					return nil, fmt.Errorf("token authorized party '%s' does not match expected client ID '%s'", azpStr, o.expectedClientId)
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
	if len(o.usernameClaim) > 0 {
		if usernameValue := o.extractClaimByPath(claimsMap, o.usernameClaim); usernameValue != "" {
			username = usernameValue
			identity.SetUsername(usernameValue)
		}
	}

	// Extract roles from claims map using role extractor
	roles := o.roleExtractor.ExtractRolesFromMap(claimsMap)
	identity.SetRoles(roles)

	// Use the stateless organization extractor with claims map
	organizations := o.organizationExtractor.ExtractOrganizations(claimsMap, username)
	reportedOrganizations := make([]common.ReportedOrganization, 0, len(organizations))
	for _, org := range organizations {
		reportedOrganizations = append(reportedOrganizations, common.ReportedOrganization{
			Name:         org,
			IsInternalID: false,
			ID:           org,
		})
	}
	identity.SetOrganizations(reportedOrganizations)

	// Set the issuer from JWT token
	if issuer := parsedToken.Issuer(); issuer != "" {
		issuer := identitypkg.NewIssuer(identitypkg.AuthTypeOIDC, issuer)
		identity.SetIssuer(issuer)
	}

	return identity, nil
}

func (o *OIDCAuth) GetIdentity(ctx context.Context, token string) (common.Identity, error) {
	identity, err := o.parseAndCreateIdentity(ctx, token)
	if err != nil {
		return nil, err
	}

	return identity, nil
}

func (o *OIDCAuth) GetAuthConfig() *api.AuthConfig {
	orgEnabled := false
	if o.orgConfig != nil {
		orgEnabled = o.orgConfig.Enabled
	}

	providerType := api.AuthProviderInfoTypeOidc
	// Use external authority for client-facing configuration
	// The external authority is what clients use to connect (may be different from internal)
	authUrl := o.externalOidcAuthority + "/authorize"
	provider := api.AuthProviderInfo{
		Name:          &o.providerName,
		DisplayName:   &o.displayName,
		Type:          &providerType,
		Issuer:        &o.externalOidcAuthority,
		AuthUrl:       &authUrl,
		ClientId:      &o.expectedClientId,
		Scopes:        &o.scopes,
		UsernameClaim: &o.usernameClaim,
		IsStatic:      lo.ToPtr(true),
	}

	return &api.AuthConfig{
		ApiVersion:           api.AuthConfigAPIVersion,
		DefaultProvider:      provider.Name,
		OrganizationsEnabled: &orgEnabled,
		Providers:            &[]api.AuthProviderInfo{provider},
	}
}

func (o *OIDCAuth) GetAuthToken(r *http.Request) (string, error) {
	return common.ExtractBearerToken(r)
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
