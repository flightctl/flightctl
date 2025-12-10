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
	"strconv"
	"strings"
	"sync"
	"time"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/auth/authn"
)

// cacheEntry holds a cached token endpoint with its expiration time
type cacheEntry struct {
	endpoint  string
	expiresAt time.Time
}

// AuthTokenProxy handles OAuth2 token exchange by proxying to configured providers
type AuthTokenProxy struct {
	authN *authn.MultiAuth

	// Cache for discovered OIDC token endpoints (issuer -> tokenEndpoint)
	tokenEndpointCache    map[string]*cacheEntry
	tokenEndpointCacheMu  sync.RWMutex
	tokenEndpointCacheTTL time.Duration

	// HTTP client for making token requests (reused across requests)
	httpClient *http.Client

	// HTTP client for OIDC discovery requests with shorter timeout (reused across requests)
	discoveryClient *http.Client
}

// NewAuthTokenProxy creates a new auth token proxy
func NewAuthTokenProxy(authN *authn.MultiAuth) *AuthTokenProxy {
	// Create HTTP client with TLS config from authN
	tlsConfig := authN.GetTLSConfig()
	if tlsConfig == nil {
		tlsConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
		}
	}

	httpClient := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}

	// Create dedicated discovery client with shorter timeout
	discoveryClient := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}

	return &AuthTokenProxy{
		authN:                 authN,
		tokenEndpointCache:    make(map[string]*cacheEntry),
		tokenEndpointCacheTTL: 5 * time.Minute, // Default 5 minute TTL for OIDC discovery
		httpClient:            httpClient,
		discoveryClient:       discoveryClient,
	}
}

// ProxyTokenRequest handles OAuth2 token exchange requests
// Returns TokenResponse and HTTP status code (200 for success, 400 for errors per OAuth2 spec)
func (p *AuthTokenProxy) ProxyTokenRequest(ctx context.Context, providerName string, tokenReq *api.TokenRequest) (*api.TokenResponse, int) {
	// Validate grant type
	if tokenReq.GrantType != api.AuthorizationCode && tokenReq.GrantType != api.RefreshToken {
		return createErrorTokenResponse("unsupported_grant_type", "Only authorization_code and refresh_token grant types are supported"), http.StatusBadRequest
	}

	// Client ID is required
	if tokenReq.ClientId == "" {
		return createErrorTokenResponse("invalid_request", "client_id is required"), http.StatusBadRequest
	}

	// Find the provider configuration
	providerConfig, clientId, clientSecret, status := p.findProviderForToken(ctx, providerName)
	if status != api.StatusOK() {
		return createErrorTokenResponse("invalid_client", "Provider not found or not configured"), http.StatusBadRequest
	}

	// Validate that the client_id from the request matches the provider's configured client_id
	if tokenReq.ClientId != clientId {
		return createErrorTokenResponse("invalid_client", "client_id does not match provider configuration"), http.StatusBadRequest
	}

	// Proxy the request to the provider's token endpoint (client_secret will be injected if configured)
	proxyResult, err := p.proxyTokenRequest(ctx, providerConfig, clientId, clientSecret, tokenReq)
	if err != nil {
		p.authN.GetLogger().Errorf("Failed to proxy token request: %v", err)
		return createErrorTokenResponse("server_error", fmt.Sprintf("Failed to proxy token request: %v", err)), http.StatusBadRequest
	}

	// OAuth2 spec: return 200 for success, 400 for all errors
	// The TokenResponse includes error fields for error cases
	httpStatus := proxyResult.StatusCode
	if httpStatus >= 200 && httpStatus < 300 {
		return proxyResult.TokenResponse, http.StatusOK
	}

	// All errors (4xx and 5xx from provider) are returned as 400 with error details in TokenResponse
	p.authN.GetLogger().Warnf("Provider returned error status %d", httpStatus)
	return proxyResult.TokenResponse, http.StatusBadRequest
}

// findProviderForToken finds the authentication provider configuration for a token request by provider name
func (p *AuthTokenProxy) findProviderForToken(ctx context.Context, providerName string) (*ProviderConfig, string, string, api.Status) {
	// Get the provider middleware directly to access internal spec (not through JSON marshaling)
	providerMiddleware, status := p.authN.GetProviderMiddleware(providerName)
	if status != api.StatusOK() || providerMiddleware == nil {
		return nil, "", "", status
	}

	// Use type switch to handle different provider types - directly access internal spec to preserve client secret
	switch provider := providerMiddleware.(type) {
	case interface{ GetOIDCSpec() api.OIDCProviderSpec }:
		oidcSpec := provider.GetOIDCSpec()

		tokenEndpoint, discoveryErr := p.discoverTokenEndpoint(oidcSpec.Issuer)
		if discoveryErr != nil {
			p.authN.GetLogger().Errorf("Failed to discover token endpoint for issuer %s: %v", oidcSpec.Issuer, discoveryErr)
			return nil, "", "", api.StatusInternalServerError("Failed to discover token endpoint")
		}

		clientSecret := ""
		if oidcSpec.ClientSecret != nil {
			clientSecret = *oidcSpec.ClientSecret
		}

		return &ProviderConfig{
			Issuer:        oidcSpec.Issuer,
			TokenEndpoint: tokenEndpoint,
			ClientId:      oidcSpec.ClientId,
		}, oidcSpec.ClientId, clientSecret, api.StatusOK()

	case interface{ GetOAuth2Spec() api.OAuth2ProviderSpec }:
		oauth2Spec := provider.GetOAuth2Spec()

		clientSecret := ""
		if oauth2Spec.ClientSecret != nil {
			clientSecret = *oauth2Spec.ClientSecret
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

	case interface {
		GetOpenShiftSpec() api.OpenShiftProviderSpec
	}:
		openshiftSpec := provider.GetOpenShiftSpec()

		clientSecret := ""
		if openshiftSpec.ClientSecret != nil {
			clientSecret = *openshiftSpec.ClientSecret
		}

		issuer := ""
		if openshiftSpec.Issuer != nil {
			issuer = *openshiftSpec.Issuer
		}

		tokenEndpoint := ""
		if openshiftSpec.TokenUrl != nil {
			tokenEndpoint = *openshiftSpec.TokenUrl
		}

		clientId := ""
		if openshiftSpec.ClientId != nil {
			clientId = *openshiftSpec.ClientId
		}

		return &ProviderConfig{
			Issuer:        issuer,
			TokenEndpoint: tokenEndpoint,
			ClientId:      clientId,
		}, clientId, clientSecret, api.StatusOK()
	case interface{ GetAapSpec() api.AapProviderSpec }:
		aapSpec := provider.GetAapSpec()
		clientSecret := ""
		if aapSpec.ClientSecret != nil {
			clientSecret = *aapSpec.ClientSecret
		}
		// Ensure token URL has trailing slash (AAP requires it)
		tokenUrl := aapSpec.TokenUrl
		if !strings.HasSuffix(tokenUrl, "/") {
			tokenUrl = tokenUrl + "/"
		}
		return &ProviderConfig{
			Issuer:        aapSpec.ApiUrl,
			TokenEndpoint: tokenUrl,
			ClientId:      aapSpec.ClientId,
			UseBasicAuth:  aapSpec.ClientSecret != nil && *aapSpec.ClientSecret != "", // AAP requires Basic Auth for client credentials
		}, aapSpec.ClientId, clientSecret, api.StatusOK()

	default:
		return nil, "", "", api.StatusBadRequest("Provider type not supported")
	}
}

// ProviderConfig holds the configuration needed to proxy requests to an OAuth2 provider
type ProviderConfig struct {
	Issuer        string
	TokenEndpoint string
	ClientId      string
	UseBasicAuth  bool // If true, send client_id/client_secret as Basic Auth header instead of form data
}

// ProxyResult holds the result of proxying a token request, including the upstream HTTP status
type ProxyResult struct {
	TokenResponse *api.TokenResponse
	StatusCode    int
}

// discoverTokenEndpoint discovers the token endpoint from an OIDC issuer's well-known configuration
// Results are cached to avoid repeated HTTP calls
func (p *AuthTokenProxy) discoverTokenEndpoint(issuer string) (string, error) {
	issuer = strings.TrimSuffix(issuer, "/")

	// Check cache first (read lock)
	p.tokenEndpointCacheMu.RLock()
	if cached, ok := p.tokenEndpointCache[issuer]; ok {
		// Check if entry has expired
		if time.Now().Before(cached.expiresAt) {
			p.tokenEndpointCacheMu.RUnlock()
			return cached.endpoint, nil
		}
	}
	p.tokenEndpointCacheMu.RUnlock()

	// Not in cache, perform discovery
	discoveryURL := issuer + "/.well-known/openid-configuration"

	resp, err := p.discoveryClient.Get(discoveryURL)
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

	// Cache the result with TTL (write lock)
	p.tokenEndpointCacheMu.Lock()
	p.tokenEndpointCache[issuer] = &cacheEntry{
		endpoint:  discovery.TokenEndpoint,
		expiresAt: time.Now().Add(p.tokenEndpointCacheTTL),
	}
	p.tokenEndpointCacheMu.Unlock()

	return discovery.TokenEndpoint, nil
}

// proxyTokenRequest makes an HTTP request to the provider's token endpoint
func (p *AuthTokenProxy) proxyTokenRequest(ctx context.Context, providerConfig *ProviderConfig, clientId string, clientSecret string, tokenReq *api.TokenRequest) (*ProxyResult, error) {
	// Prepare form data
	formData := url.Values{}
	formData.Set("grant_type", string(tokenReq.GrantType))

	// For providers using Basic Auth (like AAP), don't include credentials in form data
	if !providerConfig.UseBasicAuth {
		formData.Set("client_id", clientId)
		// Inject client_secret if configured in the provider
		if clientSecret != "" {
			formData.Set("client_secret", clientSecret)
		}
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
	if tokenReq.RedirectUri != nil {
		formData.Set("redirect_uri", *tokenReq.RedirectUri)
	}

	// Encode the form data
	encodedBody := formData.Encode()

	// Log request details
	p.authN.GetLogger().Infof("Token proxy: sending request to %s", providerConfig.TokenEndpoint)
	p.authN.GetLogger().Infof("Token proxy: UseBasicAuth=%v, grant_type=%s", providerConfig.UseBasicAuth, tokenReq.GrantType)
	p.authN.GetLogger().Debugf("Token proxy: form data: %s", encodedBody)

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, providerConfig.TokenEndpoint, bytes.NewBufferString(encodedBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json, application/x-www-form-urlencoded")

	// For providers using Basic Auth (like AAP), set the Authorization header
	// Only use Basic Auth if UseBasicAuth is true
	if providerConfig.UseBasicAuth {
		req.SetBasicAuth(clientId, clientSecret)
		p.authN.GetLogger().Debugf("Token proxy: using Basic Auth with client_id=%s", clientId)
	}

	// Make the request using the shared HTTP client
	resp, err := p.httpClient.Do(req)
	if err != nil {
		p.authN.GetLogger().Errorf("Token proxy: request failed: %v", err)
		return nil, fmt.Errorf("failed to send token request: %w", err)
	}
	defer resp.Body.Close()

	// Log response status
	p.authN.GetLogger().Infof("Token proxy: received response status %d", resp.StatusCode)
	p.authN.GetLogger().Debugf("Token proxy: response headers: %v", resp.Header)

	// Read response body
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	p.authN.GetLogger().Debugf("Token proxy: response body: [REDACTED] (length: %d bytes)", len(bodyBytes))

	// Parse response based on Content-Type
	var tokenResp api.TokenResponse
	contentType := resp.Header.Get("Content-Type")

	if strings.Contains(contentType, "application/x-www-form-urlencoded") {
		// Parse URL-encoded response (e.g., GitHub OAuth2)
		values, err := url.ParseQuery(string(bodyBytes))
		if err != nil {
			p.authN.GetLogger().Errorf("Token proxy: failed to parse URL-encoded response (status %d): %v", resp.StatusCode, err)
			return nil, fmt.Errorf("failed to parse URL-encoded token response (status %d): %w, body: <omitted>", resp.StatusCode, err)
		}

		// Map form fields to TokenResponse
		if accessToken := values.Get("access_token"); accessToken != "" {
			tokenResp.AccessToken = &accessToken
		}
		if tokenType := values.Get("token_type"); tokenType != "" {
			tt := api.TokenResponseTokenType(tokenType)
			tokenResp.TokenType = &tt
		}
		if refreshToken := values.Get("refresh_token"); refreshToken != "" {
			tokenResp.RefreshToken = &refreshToken
		}
		if idToken := values.Get("id_token"); idToken != "" {
			tokenResp.IdToken = &idToken
		}
		if expiresInStr := values.Get("expires_in"); expiresInStr != "" {
			expiresIn, err := strconv.Atoi(expiresInStr)
			if err != nil {
				p.authN.GetLogger().Warnf("Token proxy: failed to parse expires_in value '%s': %v", expiresInStr, err)
			} else {
				tokenResp.ExpiresIn = &expiresIn
			}
		}
		if errorCode := values.Get("error"); errorCode != "" {
			tokenResp.Error = &errorCode
		}
		if errorDesc := values.Get("error_description"); errorDesc != "" {
			tokenResp.ErrorDescription = &errorDesc
		}
	} else {
		// Parse JSON response (default for most OIDC providers)
		if err := json.Unmarshal(bodyBytes, &tokenResp); err != nil {
			p.authN.GetLogger().Errorf("Token proxy: failed to parse JSON response (status %d): %v", resp.StatusCode, err)
			return nil, fmt.Errorf("failed to parse JSON token response (status %d): %w, body: <omitted>", resp.StatusCode, err)
		}
	}

	return &ProxyResult{
		TokenResponse: &tokenResp,
		StatusCode:    resp.StatusCode,
	}, nil
}

// createErrorTokenResponse creates an OAuth2 error response
func createErrorTokenResponse(errorCode, errorDescription string) *api.TokenResponse {
	return &api.TokenResponse{
		Error:            &errorCode,
		ErrorDescription: &errorDescription,
	}
}
