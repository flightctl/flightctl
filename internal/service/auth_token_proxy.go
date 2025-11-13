package service

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/auth/authn"
	"github.com/google/uuid"
)

// AuthTokenProxy handles OAuth2 token exchange by proxying to configured providers
type AuthTokenProxy struct {
	authN *authn.MultiAuth

	// Cache for discovered OIDC token endpoints (issuer -> tokenEndpoint)
	tokenEndpointCache   map[string]string
	tokenEndpointCacheMu sync.RWMutex
}

// NewAuthTokenProxy creates a new auth token proxy
func NewAuthTokenProxy(authN *authn.MultiAuth) *AuthTokenProxy {
	return &AuthTokenProxy{
		authN:              authN,
		tokenEndpointCache: make(map[string]string),
	}
}

// ProxyTokenRequest handles OAuth2 token exchange requests
func (p *AuthTokenProxy) ProxyTokenRequest(ctx context.Context, orgId uuid.UUID, tokenReq *api.TokenRequest) (*api.TokenResponse, api.Status) {
	// Validate grant type
	if tokenReq.GrantType != api.AuthorizationCode && tokenReq.GrantType != api.RefreshToken {
		return createErrorTokenResponse("unsupported_grant_type", "Only authorization_code and refresh_token grant types are supported"), api.StatusBadRequest("Unsupported grant type")
	}

	// Provider name is required
	if tokenReq.ProviderName == "" {
		return createErrorTokenResponse("invalid_request", "provider_name is required"), api.StatusBadRequest("provider_name is required")
	}

	// Find the provider configuration
	providerConfig, clientId, clientSecret, status := p.findProviderForToken(ctx, tokenReq.ProviderName)
	if status != api.StatusOK() {
		return createErrorTokenResponse("invalid_client", "Provider not found or not configured"), status
	}

	// Proxy the request to the provider's token endpoint (client_secret will be injected if configured)
	tokenResp, err := p.proxyTokenRequest(ctx, providerConfig.TokenEndpoint, clientId, clientSecret, tokenReq)
	if err != nil {
		p.authN.GetLogger().Errorf("Failed to proxy token request: %v", err)
		return createErrorTokenResponse("server_error", "Failed to proxy token request"), api.StatusInternalServerError(err.Error())
	}

	return tokenResp, api.StatusOK()
}

// findProviderForToken finds the authentication provider configuration for a token request by provider name
func (p *AuthTokenProxy) findProviderForToken(ctx context.Context, providerName string) (*ProviderConfig, string, string, api.Status) {
	// Get provider from authN (which checks both static and dynamic providers)
	provider, status := p.authN.GetAuthProviderByName(ctx, providerName)
	if status != api.StatusOK() || provider == nil {
		return nil, "", "", status
	}

	// Try OIDC provider
	if oidcSpec, err := provider.Spec.AsOIDCProviderSpec(); err == nil {
		// Found matching OIDC provider
		tokenEndpoint, discoveryErr := p.discoverTokenEndpoint(oidcSpec.Issuer)
		if discoveryErr != nil {
			p.authN.GetLogger().Errorf("Failed to discover token endpoint for issuer %s: %v", oidcSpec.Issuer, discoveryErr)
			return nil, "", "", api.StatusInternalServerError("Failed to discover token endpoint")
		}

		clientSecret := ""
		if oidcSpec.ClientSecret != nil {
			clientSecret = string(*oidcSpec.ClientSecret)
		}

		return &ProviderConfig{
			Issuer:        oidcSpec.Issuer,
			TokenEndpoint: tokenEndpoint,
			ClientId:      oidcSpec.ClientId,
		}, oidcSpec.ClientId, clientSecret, api.StatusOK()
	}

	// Try OAuth2 provider
	if oauth2Spec, err := provider.Spec.AsOAuth2ProviderSpec(); err == nil {
		// Found matching OAuth2 provider
		clientSecret := ""
		if oauth2Spec.ClientSecret != nil {
			clientSecret = string(*oauth2Spec.ClientSecret)
		}

		issuer := ""
		if oauth2Spec.Issuer != nil {
			issuer = *oauth2Spec.Issuer
		}

		return &ProviderConfig{
			Issuer:        issuer,
			TokenEndpoint: oauth2Spec.TokenUrl,
			ClientId:      oauth2Spec.ClientId,
		}, oauth2Spec.ClientId, clientSecret, api.StatusOK()
	}

	return nil, "", "", api.StatusBadRequest("Provider type not supported")
}

// ProviderConfig holds the configuration needed to proxy requests to an OAuth2 provider
type ProviderConfig struct {
	Issuer        string
	TokenEndpoint string
	ClientId      string
}

// discoverTokenEndpoint discovers the token endpoint from an OIDC issuer's well-known configuration
// Results are cached to avoid repeated HTTP calls
func (p *AuthTokenProxy) discoverTokenEndpoint(issuer string) (string, error) {
	issuer = strings.TrimSuffix(issuer, "/")

	// Check cache first (read lock)
	p.tokenEndpointCacheMu.RLock()
	if cached, ok := p.tokenEndpointCache[issuer]; ok {
		p.tokenEndpointCacheMu.RUnlock()
		return cached, nil
	}
	p.tokenEndpointCacheMu.RUnlock()

	// Not in cache, perform discovery
	discoveryURL := issuer + "/.well-known/openid-configuration"

	// Get TLS config from authN
	tlsConfig := p.authN.GetTLSConfig()
	if tlsConfig == nil {
		tlsConfig = &tls.Config{
			InsecureSkipVerify: false, //nolint:gosec
		}
	}

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}

	resp, err := client.Get(discoveryURL)
	if err != nil {
		return "", fmt.Errorf("failed to fetch OIDC discovery document: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("OIDC discovery returned status %d", resp.StatusCode)
	}

	var discovery struct {
		TokenEndpoint string `json:"token_endpoint"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&discovery); err != nil {
		return "", fmt.Errorf("failed to parse OIDC discovery document: %w", err)
	}

	if discovery.TokenEndpoint == "" {
		return "", fmt.Errorf("token_endpoint not found in OIDC discovery document")
	}

	// Cache the result (write lock)
	p.tokenEndpointCacheMu.Lock()
	p.tokenEndpointCache[issuer] = discovery.TokenEndpoint
	p.tokenEndpointCacheMu.Unlock()

	return discovery.TokenEndpoint, nil
}

// proxyTokenRequest makes an HTTP request to the provider's token endpoint
func (p *AuthTokenProxy) proxyTokenRequest(ctx context.Context, tokenEndpoint string, clientId string, clientSecret string, tokenReq *api.TokenRequest) (*api.TokenResponse, error) {
	// Prepare form data
	formData := url.Values{}
	formData.Set("grant_type", string(tokenReq.GrantType))
	formData.Set("client_id", clientId)

	// Inject client_secret if configured in the provider
	if clientSecret != "" {
		formData.Set("client_secret", clientSecret)
	}

	if tokenReq.Code != nil {
		formData.Set("code", *tokenReq.Code)
	}
	if tokenReq.RefreshToken != nil {
		formData.Set("refresh_token", *tokenReq.RefreshToken)
	}
	if tokenReq.Scope != nil {
		formData.Set("scope", *tokenReq.Scope)
	}
	if tokenReq.CodeVerifier != nil {
		formData.Set("code_verifier", *tokenReq.CodeVerifier)
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, bytes.NewBufferString(formData.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	// Get TLS config from authN
	tlsConfig := p.authN.GetTLSConfig()
	if tlsConfig == nil {
		tlsConfig = &tls.Config{
			InsecureSkipVerify: false, //nolint:gosec
		}
	}

	// Make the request
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send token request: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// Parse response
	var tokenResp api.TokenResponse
	if err := json.Unmarshal(bodyBytes, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	return &tokenResp, nil
}

// createErrorTokenResponse creates an OAuth2 error response
func createErrorTokenResponse(errorCode, errorDescription string) *api.TokenResponse {
	return &api.TokenResponse{
		Error:            &errorCode,
		ErrorDescription: &errorDescription,
	}
}
