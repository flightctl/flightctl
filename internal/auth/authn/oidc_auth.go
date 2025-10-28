package authn

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	oidcAuthority         string
	jwksUri               string
	clientTlsConfig       *tls.Config
	client                *http.Client
	orgConfig             *common.AuthOrganizationsConfig
	usernameClaim         string
	groupsClaim           string
	expectedClientId      string
	scopes                []string
	jwksCache             *jwk.Cache
	organizationExtractor *OrganizationExtractor
}

type OIDCServerResponse struct {
	TokenEndpoint string `json:"token_endpoint"`
	JwksUri       string `json:"jwks_uri"`
}

func NewOIDCAuth(providerName string, oidcAuthority string, clientTlsConfig *tls.Config, orgConfig *common.AuthOrganizationsConfig, usernameClaim string, groupsClaim string, expectedClientId string, scopes []string) (*OIDCAuth, error) {
	oidcAuth := &OIDCAuth{
		providerName:    providerName,
		oidcAuthority:   oidcAuthority,
		clientTlsConfig: clientTlsConfig,
		client: &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: clientTlsConfig,
			},
		},
		orgConfig:        orgConfig,
		usernameClaim:    usernameClaim,
		groupsClaim:      groupsClaim,
		expectedClientId: expectedClientId,
		scopes:           scopes,
	}

	res, err := oidcAuth.client.Get(fmt.Sprintf("%s/.well-known/openid-configuration", oidcAuthority))
	if err != nil {
		return oidcAuth, err
	}
	oidcResponse := OIDCServerResponse{}
	defer res.Body.Close()
	bodyBytes, err := io.ReadAll(res.Body)
	if err != nil {
		return oidcAuth, err
	}
	if err := json.Unmarshal(bodyBytes, &oidcResponse); err != nil {
		return oidcAuth, err
	}
	oidcAuth.jwksUri = oidcResponse.JwksUri

	// Initialize JWKS cache with 15-minute refresh interval
	// This balances performance with key rotation requirements
	oidcAuth.jwksCache = jwk.NewCache(context.Background())
	oidcAuth.jwksCache.Register(oidcAuth.jwksUri, jwk.WithMinRefreshInterval(15*time.Minute))

	// Create stateless organization extractor
	oidcAuth.organizationExtractor = NewOrganizationExtractor(orgConfig)

	return oidcAuth, nil
}

func (o OIDCAuth) ValidateToken(ctx context.Context, token string) error {
	_, err := o.parseAndCreateIdentity(ctx, token)
	return err
}

func (o OIDCAuth) parseAndCreateIdentity(ctx context.Context, token string) (*JWTIdentity, error) {
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
		if aud, exists := parsedToken.Get("aud"); exists {
			switch audValue := aud.(type) {
			case string:
				// Single audience - must match exactly
				if audValue == o.expectedClientId {
					audienceValid = true
				}
			case []interface{}:
				// Multiple audiences - check if our client ID is in the list
				for _, v := range audValue {
					if audStr, ok := v.(string); ok {
						if audStr == o.expectedClientId {
							audienceValid = true
							break
						}
					}
				}
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

	if o.usernameClaim != "" {
		if username, exists := parsedToken.Get(o.usernameClaim); exists {
			if usernameStr, ok := username.(string); ok {
				identity.SetUsername(usernameStr)
			}
		}
	}

	// Extract roles from JWT
	roles := o.extractRoles(parsedToken)
	identity.SetRoles(roles)

	// Extract organizations from JWT based on org config
	username := ""
	if o.usernameClaim != "" {
		if usernameClaim, exists := parsedToken.Get(o.usernameClaim); exists {
			if usernameStr, ok := usernameClaim.(string); ok {
				username = usernameStr
			}
		}
	}

	// Convert JWT token to claims map for organization extraction
	claimsMap, err := parsedToken.AsMap(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to convert JWT token to map: %w", err)
	}

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
	issuerID := ""
	if iss, exists := parsedToken.Get("iss"); exists {
		if issStr, ok := iss.(string); ok {
			issuerID = issStr
		}
	}
	issuer := identitypkg.NewIssuer(identitypkg.AuthTypeOIDC, issuerID)
	identity.SetIssuer(issuer)

	return identity, nil
}

func (o OIDCAuth) GetIdentity(ctx context.Context, token string) (common.Identity, error) {
	identity, err := o.parseAndCreateIdentity(ctx, token)
	if err != nil {
		return nil, err
	}

	return identity, nil
}

func (o OIDCAuth) GetAuthConfig() *api.AuthConfig {
	orgEnabled := false
	if o.orgConfig != nil {
		orgEnabled = o.orgConfig.Enabled
	}

	providerType := string(api.AuthProviderInfoTypeOidc)
	provider := api.AuthProviderInfo{
		Name:          &o.providerName,
		Type:          (*api.AuthProviderInfoType)(&providerType),
		Issuer:        &o.oidcAuthority,
		ClientId:      &o.expectedClientId,
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

func (o OIDCAuth) GetAuthToken(r *http.Request) (string, error) {
	return common.ExtractBearerToken(r)
}

// extractRoles extracts roles from multiple possible JWT claims
func (o OIDCAuth) extractRoles(token jwt.Token) []string {
	var roles []string

	// 1. Try configured groups claim first
	if o.groupsClaim != "" {
		if groupsClaim, exists := token.Get(o.groupsClaim); exists {
			if groupsList, ok := groupsClaim.([]interface{}); ok {
				for _, group := range groupsList {
					if groupStr, ok := group.(string); ok {
						roles = append(roles, groupStr)
					}
				}
			}
		}
	}

	return roles
}
