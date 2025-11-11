package authn

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/lestrrat-go/jwx/v2/jwt"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

// Organization assignment type constants
const (
	OrganizationAssignmentTypeStatic  = "static"
	OrganizationAssignmentTypeDynamic = "dynamic"
	OrganizationAssignmentTypePerUser = "perUser"
)

// contextKey is a custom type for context keys to avoid collisions
type contextKey string

// ParsedTokenCtxKey is the context key for the parsed JWT token
const ParsedTokenCtxKey contextKey = "parsed_token"

// GetParsedTokenFromContext retrieves the parsed JWT token from the context if it exists
func GetParsedTokenFromContext(ctx context.Context) (jwt.Token, bool) {
	if token, ok := ctx.Value(ParsedTokenCtxKey).(jwt.Token); ok {
		return token, true
	}
	return nil, false
}

// TokenType represents the type of JWT authentication token
type TokenType int

const (
	TokenTypeOIDC TokenType = iota
	TokenTypeK8s
)

// AuthProviderService interface for auth provider operations
type AuthProviderService interface {
	ListAuthProviders(ctx context.Context, params api.ListAuthProvidersParams) (*api.AuthProviderList, api.Status)
	GetAuthProviderByIssuerAndClientId(ctx context.Context, issuer string, clientId string) (*api.AuthProvider, api.Status)
}

// AuthProviderCacheKey is a composite key for caching auth providers
type AuthProviderCacheKey struct {
	Issuer   string
	ClientId string
}

// MultiAuth implements authentication using multiple providers with issuer-based routing
type MultiAuth struct {
	// Static providers - initialized once at startup, mapped by issuer
	staticProviders map[string]common.AuthNMiddleware // issuer -> provider mapping

	// Service for dynamic auth providers
	authProviderService AuthProviderService

	// TLS config for OIDC provider connections
	tlsConfig *tls.Config

	// Logger for authentication operations
	log logrus.FieldLogger

	// Dynamic OIDC providers - issuer+clientId -> provider mapping
	dynamicProviders   map[AuthProviderCacheKey]common.AuthNMiddleware
	dynamicProvidersMu sync.RWMutex

	// Control for background loader goroutine
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewMultiAuth creates a new MultiAuth instance
func NewMultiAuth(authProviderService AuthProviderService, tlsConfig *tls.Config, log logrus.FieldLogger) *MultiAuth {
	ctx, cancel := context.WithCancel(context.Background())

	m := &MultiAuth{
		staticProviders:     make(map[string]common.AuthNMiddleware),
		authProviderService: authProviderService,
		tlsConfig:           tlsConfig,
		log:                 log,
		dynamicProviders:    make(map[AuthProviderCacheKey]common.AuthNMiddleware),
		ctx:                 ctx,
		cancel:              cancel,
	}

	return m
}

// AddStaticProvider adds a static authentication provider with its issuer
func (m *MultiAuth) AddStaticProvider(issuer string, provider common.AuthNMiddleware) {
	m.staticProviders[issuer] = provider
}

// HasProviders returns true if any providers are configured
func (m *MultiAuth) HasProviders() bool {
	return len(m.staticProviders) > 0
}

// Start starts the background loader goroutine
func (m *MultiAuth) Start() {

	m.wg.Add(1)
	go m.periodicLoader()
}

// Stop stops the background loader goroutine
func (m *MultiAuth) Stop() {
	m.cancel()
	m.wg.Wait()
}

// periodicLoader runs in the background and reloads dynamic providers every 5 seconds
func (m *MultiAuth) periodicLoader() {
	defer m.wg.Done()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// Load immediately on start
	if err := m.LoadAllAuthProviders(m.ctx); err != nil {
		m.log.Warnf("Failed to load auth providers on startup: %v", err)
	}

	for {
		select {
		case <-ticker.C:
			if err := m.LoadAllAuthProviders(m.ctx); err != nil {
				m.log.Warnf("Failed to reload auth providers: %v", err)
			}
		case <-m.ctx.Done():
			m.log.Info("Stopping auth provider loader")
			return
		}
	}
}

// LoadAllAuthProviders reloads auth providers from the database with change detection
func (m *MultiAuth) LoadAllAuthProviders(ctx context.Context) error {

	// List all auth providers from database
	providerList, status := m.authProviderService.ListAuthProviders(ctx, api.ListAuthProvidersParams{})
	if status.Code != http.StatusOK {
		return fmt.Errorf("failed to list auth providers: %v", status)
	}

	// Build map of provider keys from DB for tracking
	dbProviderKeys := make(map[AuthProviderCacheKey]*api.AuthProvider)
	if providerList != nil && len(providerList.Items) > 0 {
		for i := range providerList.Items {
			provider := &providerList.Items[i]
			providerKey, err := m.getProviderKey(provider)
			if err != nil {
				m.log.Warnf("Failed to get key for auth provider %s: %v", lo.FromPtr(provider.Metadata.Name), err)
				continue
			}
			dbProviderKeys[providerKey] = provider
		}
	}

	m.dynamicProvidersMu.Lock()
	defer m.dynamicProvidersMu.Unlock()

	// Track which providers we've seen
	processedKeys := make(map[AuthProviderCacheKey]bool)
	addedCount := 0
	updatedCount := 0
	unchangedCount := 0

	// Process all providers from DB
	for providerKey, provider := range dbProviderKeys {
		processedKeys[providerKey] = true

		existingMiddleware, exists := m.dynamicProviders[providerKey]

		if exists {
			// Provider exists - check if it changed
			changed, err := m.hasProviderChanged(existingMiddleware, provider)
			if err != nil {
				m.log.Warnf("Failed to check if provider %s changed: %v", lo.FromPtr(provider.Metadata.Name), err)
				continue
			}

			if changed {
				// Provider changed - reconstruct middleware
				_, authMiddleware, err := m.createAuthMiddlewareFromProvider(provider)
				if err != nil {
					m.log.Warnf("Failed to update auth provider %s: %v", lo.FromPtr(provider.Metadata.Name), err)
					continue
				}
				m.dynamicProviders[providerKey] = authMiddleware
				m.log.Infof("Updated auth provider: %s", lo.FromPtr(provider.Metadata.Name))
				updatedCount++
			} else {
				// Provider unchanged - keep existing
				unchangedCount++
			}
		} else {
			// New provider - create and add
			_, authMiddleware, err := m.createAuthMiddlewareFromProvider(provider)
			if err != nil {
				m.log.Warnf("Failed to create auth provider %s: %v", lo.FromPtr(provider.Metadata.Name), err)
				continue
			}
			m.dynamicProviders[providerKey] = authMiddleware
			m.log.Infof("Added new auth provider: %s", lo.FromPtr(provider.Metadata.Name))
			addedCount++
		}
	}

	// Remove providers that are no longer in DB
	removedCount := 0
	for providerKey := range m.dynamicProviders {
		if !processedKeys[providerKey] {
			delete(m.dynamicProviders, providerKey)
			m.log.Infof("Removed auth provider: issuer=%s, clientId=%s", providerKey.Issuer, providerKey.ClientId)
			removedCount++
		}
	}

	m.log.Debugf("Provider sync complete: %d total, %d added, %d updated, %d unchanged, %d removed",
		len(m.dynamicProviders), addedCount, updatedCount, unchangedCount, removedCount)
	return nil
}

// getProviderKey extracts the cache key from a provider without creating middleware
func (m *MultiAuth) getProviderKey(provider *api.AuthProvider) (AuthProviderCacheKey, error) {
	discriminator, err := provider.Spec.Discriminator()
	if err != nil {
		return AuthProviderCacheKey{}, fmt.Errorf("failed to determine provider type: %w", err)
	}

	switch discriminator {
	case string(api.Oidc):
		oidcSpec, err := provider.Spec.AsOIDCProviderSpec()
		if err != nil {
			return AuthProviderCacheKey{}, fmt.Errorf("failed to parse OIDC provider spec: %w", err)
		}
		return AuthProviderCacheKey{Issuer: oidcSpec.Issuer, ClientId: oidcSpec.ClientId}, nil

	case string(api.OAuth2ProviderSpecProviderTypeOauth2):
		oauth2Spec, err := provider.Spec.AsOAuth2ProviderSpec()
		if err != nil {
			return AuthProviderCacheKey{}, fmt.Errorf("failed to parse OAuth2 provider spec: %w", err)
		}
		issuer := lo.FromPtr(oauth2Spec.Issuer)
		if issuer == "" {
			issuer = oauth2Spec.AuthorizationUrl
		}
		return AuthProviderCacheKey{Issuer: issuer, ClientId: oauth2Spec.ClientId}, nil

	default:
		return AuthProviderCacheKey{}, fmt.Errorf("unsupported provider type: %s", discriminator)
	}
}

// hasProviderChanged checks if a provider's configuration has changed
//
//nolint:gocyclo // Function complexity is acceptable for provider comparison
func (m *MultiAuth) hasProviderChanged(existingMiddleware common.AuthNMiddleware, newProvider *api.AuthProvider) (bool, error) {
	// Get the auth config from existing middleware
	existingConfig := existingMiddleware.GetAuthConfig()
	if existingConfig == nil || existingConfig.Providers == nil || len(*existingConfig.Providers) == 0 {
		return true, nil // If we can't get config, assume changed
	}

	existingProviderInfo := (*existingConfig.Providers)[0]

	// Determine provider type
	discriminator, err := newProvider.Spec.Discriminator()
	if err != nil {
		return true, err
	}

	// Compare based on provider type
	switch discriminator {
	case string(api.Oidc):
		oidcSpec, err := newProvider.Spec.AsOIDCProviderSpec()
		if err != nil {
			return true, err
		}

		// Compare relevant fields
		if existingProviderInfo.Issuer != nil && *existingProviderInfo.Issuer != oidcSpec.Issuer {
			return true, nil
		}
		if existingProviderInfo.ClientId != nil && *existingProviderInfo.ClientId != oidcSpec.ClientId {
			return true, nil
		}
		if existingProviderInfo.DisplayName != nil && oidcSpec.DisplayName != nil && *existingProviderInfo.DisplayName != *oidcSpec.DisplayName {
			return true, nil
		}
		if !equalStringSlices(existingProviderInfo.UsernameClaim, oidcSpec.UsernameClaim) {
			return true, nil
		}
		// Compare scopes
		if !equalScopes(existingProviderInfo.Scopes, oidcSpec.Scopes) {
			return true, nil
		}

	case string(api.OAuth2ProviderSpecProviderTypeOauth2):
		oauth2Spec, err := newProvider.Spec.AsOAuth2ProviderSpec()
		if err != nil {
			return true, err
		}

		// Compare relevant fields
		if existingProviderInfo.Issuer != nil && oauth2Spec.Issuer != nil && *existingProviderInfo.Issuer != *oauth2Spec.Issuer {
			return true, nil
		}
		if (existingProviderInfo.Issuer == nil) != (oauth2Spec.Issuer == nil) {
			return true, nil
		}
		if existingProviderInfo.AuthUrl != nil && *existingProviderInfo.AuthUrl != oauth2Spec.AuthorizationUrl {
			return true, nil
		}
		if existingProviderInfo.TokenUrl != nil && *existingProviderInfo.TokenUrl != oauth2Spec.TokenUrl {
			return true, nil
		}
		if existingProviderInfo.UserinfoUrl != nil && *existingProviderInfo.UserinfoUrl != oauth2Spec.UserinfoUrl {
			return true, nil
		}
		if existingProviderInfo.ClientId != nil && *existingProviderInfo.ClientId != oauth2Spec.ClientId {
			return true, nil
		}
		if existingProviderInfo.DisplayName != nil && oauth2Spec.DisplayName != nil && *existingProviderInfo.DisplayName != *oauth2Spec.DisplayName {
			return true, nil
		}
		if !equalStringSlices(existingProviderInfo.UsernameClaim, oauth2Spec.UsernameClaim) {
			return true, nil
		}
		// Compare scopes
		if !equalScopes(existingProviderInfo.Scopes, oauth2Spec.Scopes) {
			return true, nil
		}

	default:
		return true, fmt.Errorf("unsupported provider type: %s", discriminator)
	}

	return false, nil
}

// equalScopes compares two scope arrays
func equalScopes(a *[]string, b *[]string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	if len(*a) != len(*b) {
		return false
	}

	// Create maps for comparison
	aMap := make(map[string]bool)
	for _, scope := range *a {
		aMap[scope] = true
	}
	for _, scope := range *b {
		if !aMap[scope] {
			return false
		}
	}
	return true
}

// equalStringSlices compares two string slice arrays
func equalStringSlices(a *[]string, b *[]string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	if len(*a) != len(*b) {
		return false
	}

	for i := range *a {
		if (*a)[i] != (*b)[i] {
			return false
		}
	}
	return true
}

// createAuthMiddlewareFromProvider creates an auth middleware from a provider and returns the cache key
func (m *MultiAuth) createAuthMiddlewareFromProvider(provider *api.AuthProvider) (AuthProviderCacheKey, common.AuthNMiddleware, error) {
	// Determine provider type
	discriminator, err := provider.Spec.Discriminator()
	if err != nil {
		return AuthProviderCacheKey{}, nil, fmt.Errorf("failed to determine provider type: %w", err)
	}

	// Create the auth middleware
	method, err := createAuthFromProvider(provider, m.tlsConfig, m.log)
	if err != nil {
		return AuthProviderCacheKey{}, nil, fmt.Errorf("failed to create auth provider: %w", err)
	}

	// Get the cache key based on provider type
	var providerKey AuthProviderCacheKey
	switch discriminator {
	case string(api.Oidc):
		oidcSpec, err := provider.Spec.AsOIDCProviderSpec()
		if err != nil {
			return AuthProviderCacheKey{}, nil, fmt.Errorf("failed to parse OIDC provider spec: %w", err)
		}
		providerKey = AuthProviderCacheKey{Issuer: oidcSpec.Issuer, ClientId: oidcSpec.ClientId}

	case string(api.OAuth2ProviderSpecProviderTypeOauth2):
		oauth2Spec, err := provider.Spec.AsOAuth2ProviderSpec()
		if err != nil {
			return AuthProviderCacheKey{}, nil, fmt.Errorf("failed to parse OAuth2 provider spec: %w", err)
		}
		issuer := lo.FromPtr(oauth2Spec.Issuer)
		if issuer == "" {
			issuer = oauth2Spec.AuthorizationUrl
		}
		providerKey = AuthProviderCacheKey{Issuer: issuer, ClientId: oauth2Spec.ClientId}

	default:
		return AuthProviderCacheKey{}, nil, fmt.Errorf("unsupported provider type: %s", discriminator)
	}

	return providerKey, method, nil
}

// getDynamicAuthProvider gets a cached auth provider
func (m *MultiAuth) getDynamicAuthProvider(issuer string, clientId string) (common.AuthNMiddleware, bool) {
	providerKey := AuthProviderCacheKey{Issuer: issuer, ClientId: clientId}

	m.dynamicProvidersMu.RLock()
	defer m.dynamicProvidersMu.RUnlock()

	provider, exists := m.dynamicProviders[providerKey]
	return provider, exists
}

// ValidateToken validates a token using issuer-based routing
func (m *MultiAuth) ValidateToken(ctx context.Context, token string) error {
	// Get possible providers for this token
	providers, parsedToken, err := m.getPossibleProviders(token)
	if err != nil {
		return err
	}

	// Add parsed token to context if it exists
	if parsedToken != nil {
		ctx = context.WithValue(ctx, ParsedTokenCtxKey, parsedToken)
	}

	// Try each provider until one validates successfully
	for _, provider := range providers {
		if err := provider.ValidateToken(ctx, token); err == nil {
			return nil
		}
	}

	return fmt.Errorf("token validation failed against all providers")
}

// GetIdentity extracts identity from a token using issuer-based routing
func (m *MultiAuth) GetIdentity(ctx context.Context, token string) (common.Identity, error) {
	// Get possible providers for this token
	providers, parsedToken, err := m.getPossibleProviders(token)
	if err != nil {
		return nil, err
	}

	// Add parsed token to context if it exists
	if parsedToken != nil {
		ctx = context.WithValue(ctx, ParsedTokenCtxKey, parsedToken)
	}

	// Try each provider until one returns identity successfully
	for _, provider := range providers {
		if identity, err := provider.GetIdentity(ctx, token); err == nil {
			return identity, nil
		}
	}

	return nil, fmt.Errorf("no identity found for token")
}

// GetAuthToken extracts the auth token from the request
func (m *MultiAuth) GetAuthToken(r *http.Request) (string, error) {
	return common.ExtractBearerToken(r)
}

// GetAuthConfig returns the auth configuration with all available providers
func (m *MultiAuth) GetAuthConfig() *api.AuthConfig {
	allProviders := []api.AuthProviderInfo{}
	var defaultProviderName string
	var orgEnabled bool

	// Collect all static providers
	isFirst := true
	for _, provider := range m.staticProviders {
		config := provider.GetAuthConfig()

		// Get org config from first provider
		if isFirst && config.OrganizationsEnabled != nil {
			orgEnabled = *config.OrganizationsEnabled
		}

		// Add all providers from this config
		if config.Providers != nil {
			for _, providerInfo := range *config.Providers {
				// Set static flag
				providerInfo.IsStatic = lo.ToPtr(true)

				// Set default provider name from first provider
				if isFirst && providerInfo.Name != nil {
					defaultProviderName = *providerInfo.Name
				}

				allProviders = append(allProviders, providerInfo)
				isFirst = false
			}
		}
	}

	// Collect all dynamic providers
	m.dynamicProvidersMu.RLock()
	for _, provider := range m.dynamicProviders {
		config := provider.GetAuthConfig()

		// Add all providers from this config
		if config.Providers != nil {
			for _, providerInfo := range *config.Providers {
				// Set static flag for dynamic providers
				providerInfo.IsStatic = lo.ToPtr(false)

				allProviders = append(allProviders, providerInfo)
			}
		}
	}
	m.dynamicProvidersMu.RUnlock()

	// If no providers found, return config with nil default provider
	if len(allProviders) == 0 {
		return &api.AuthConfig{
			ApiVersion:           api.AuthConfigAPIVersion,
			DefaultProvider:      nil,
			OrganizationsEnabled: &orgEnabled,
			Providers:            &allProviders,
		}
	}

	return &api.AuthConfig{
		ApiVersion:           api.AuthConfigAPIVersion,
		DefaultProvider:      &defaultProviderName,
		OrganizationsEnabled: &orgEnabled,
		Providers:            &allProviders,
	}
}

// getPossibleProviders extracts possible providers from a token
// Returns a list of providers and the parsed JWT token (nil if not a JWT)
func (m *MultiAuth) getPossibleProviders(token string) ([]common.AuthNMiddleware, jwt.Token, error) {
	// Try to parse as JWT token
	parsedToken, err := parseToken(token)
	if err != nil || parsedToken.Issuer() == "" {
		// Not a JWT token or JWT without issuer - return all possible providers for opaque tokens
		providers := []common.AuthNMiddleware{}

		// Add all static providers
		for _, provider := range m.staticProviders {
			providers = append(providers, provider)
		}
		// Add all cached dynamic providers
		m.dynamicProvidersMu.RLock()
		for _, provider := range m.dynamicProviders {
			providers = append(providers, provider)
		}
		m.dynamicProvidersMu.RUnlock()

		return providers, nil, nil
	}

	// Detect token type
	tokenType := detectTokenType(parsedToken)

	switch tokenType {
	case TokenTypeK8s:
		// K8s tokens: use static "k8s" key
		if provider, exists := m.staticProviders["k8s"]; exists {
			return []common.AuthNMiddleware{provider}, parsedToken, nil
		}
		return []common.AuthNMiddleware{}, parsedToken, fmt.Errorf("no K8s provider found")

	case TokenTypeOIDC:
		// OIDC tokens: collect all providers matching issuer+clientId
		issuer := parsedToken.Issuer()
		clientIds := parsedToken.Audience()
		if len(clientIds) == 0 {
			return []common.AuthNMiddleware{}, parsedToken, fmt.Errorf("OIDC token missing audience claim")
		}

		providers := []common.AuthNMiddleware{}
		// Try each client ID to find matching providers
		for _, clientId := range clientIds {
			// 1. Check static config-based providers (using string key)
			staticKey := fmt.Sprintf("%s:%s", issuer, clientId)
			if provider, exists := m.staticProviders[staticKey]; exists {
				providers = append(providers, provider)
			}

			// 2. Check cached dynamic auth providers (pre-loaded and synced via events)
			if provider, exists := m.getDynamicAuthProvider(issuer, clientId); exists {
				providers = append(providers, provider)
			}
		}

		if len(providers) == 0 {
			return []common.AuthNMiddleware{}, parsedToken, fmt.Errorf("no OIDC provider found for issuer: %s, clientIds: %v", issuer, clientIds)
		}

		return providers, parsedToken, nil

	default:
		return []common.AuthNMiddleware{}, parsedToken, fmt.Errorf("unknown token type")
	}
}

// detectTokenType determines the type of JWT token based on its claims
func detectTokenType(parsedToken jwt.Token) TokenType {
	issuer := parsedToken.Issuer()

	// Check for K8s tokens
	if strings.Contains(issuer, "kubernetes") || strings.Contains(issuer, "k8s") {
		return TokenTypeK8s
	}

	// Default to OIDC for other JWT tokens
	return TokenTypeOIDC
}

// parseToken parses a JWT token without validation
func parseToken(token string) (jwt.Token, error) {
	// Try to parse as JWT (without validation)
	parsedToken, err := jwt.Parse([]byte(token), jwt.WithVerify(false), jwt.WithValidate(false))
	if err != nil {
		return nil, fmt.Errorf("not a JWT token: %w", err)
	}
	return parsedToken, nil
}

// createAuthFromProvider creates an appropriate auth instance from a database provider
func createAuthFromProvider(provider *api.AuthProvider, tlsConfig *tls.Config, log logrus.FieldLogger) (common.AuthNMiddleware, error) {
	// Get the discriminator to determine the provider type
	discriminator, err := provider.Spec.Discriminator()
	if err != nil {
		return nil, fmt.Errorf("failed to determine provider type: %w", err)
	}

	switch discriminator {
	case string(api.Oidc):
		return createOIDCAuthFromProvider(provider, tlsConfig)
	case string(api.OAuth2ProviderSpecProviderTypeOauth2):
		return createOAuth2AuthFromProvider(provider, tlsConfig, log)
	default:
		return nil, fmt.Errorf("unsupported provider type: %s", discriminator)
	}
}

// createOIDCAuthFromProvider creates an OIDCAuth instance from an OIDC provider
func createOIDCAuthFromProvider(provider *api.AuthProvider, tlsConfig *tls.Config) (common.AuthNMiddleware, error) {
	oidcSpec, err := provider.Spec.AsOIDCProviderSpec()
	if err != nil {
		return nil, fmt.Errorf("failed to parse OIDC provider spec: %w", err)
	}

	usernameClaim := []string{"preferred_username"}
	if oidcSpec.UsernameClaim != nil && len(*oidcSpec.UsernameClaim) > 0 {
		usernameClaim = *oidcSpec.UsernameClaim
	}

	// Convert organization assignment to org config
	orgConfig := convertOrganizationAssignmentToOrgConfig(oidcSpec.OrganizationAssignment)

	// Create role extractor from role assignment
	roleExtractor := NewRoleExtractor(oidcSpec.RoleAssignment)

	// Handle scopes - convert from *[]string to []string
	var scopes []string
	if oidcSpec.Scopes != nil {
		scopes = *oidcSpec.Scopes
	}

	// Get display name or fallback to provider name
	displayName := lo.FromPtr(provider.Metadata.Name)
	if oidcSpec.DisplayName != nil && *oidcSpec.DisplayName != "" {
		displayName = *oidcSpec.DisplayName
	}

	// Create OIDCAuth instance for this specific provider
	oidcAuth, err := NewOIDCAuth(
		lo.FromPtr(provider.Metadata.Name), // Provider name from metadata
		displayName,                        // Display name from spec or fallback to provider name
		oidcSpec.Issuer,                    // Issuer for backend operations
		oidcSpec.Issuer,                    // External issuer (same as internal for dynamic providers)
		tlsConfig,                          // Use TLS config from MultiAuth
		orgConfig,                          // Use org config from provider spec
		usernameClaim,                      // Use username claim from provider spec
		roleExtractor,                      // Use role extractor from role assignment
		oidcSpec.ClientId,                  // Use client ID for audience validation
		scopes,                             // Use scopes from provider spec
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create OIDC auth for provider %s: %w",
			lo.FromPtr(provider.Metadata.Name), err)
	}

	return oidcAuth, nil
}

// createOAuth2AuthFromProvider creates an OAuth2Auth instance from an OAuth2 provider
func createOAuth2AuthFromProvider(provider *api.AuthProvider, tlsConfig *tls.Config, log logrus.FieldLogger) (common.AuthNMiddleware, error) {
	oauth2Spec, err := provider.Spec.AsOAuth2ProviderSpec()
	if err != nil {
		return nil, fmt.Errorf("failed to parse OAuth2 provider spec: %w", err)
	}

	usernameClaim := []string{"preferred_username"}
	if oauth2Spec.UsernameClaim != nil && len(*oauth2Spec.UsernameClaim) > 0 {
		usernameClaim = *oauth2Spec.UsernameClaim
	}

	// Convert organization assignment to org config
	orgConfig := convertOrganizationAssignmentToOrgConfig(oauth2Spec.OrganizationAssignment)

	// Create role extractor from role assignment
	roleExtractor := NewRoleExtractor(oauth2Spec.RoleAssignment)

	// Handle scopes - convert from *[]string to []string
	var scopes []string
	if oauth2Spec.Scopes != nil {
		scopes = *oauth2Spec.Scopes
	}

	// Get display name or fallback to provider name
	displayName := lo.FromPtr(provider.Metadata.Name)
	if oauth2Spec.DisplayName != nil && *oauth2Spec.DisplayName != "" {
		displayName = *oauth2Spec.DisplayName
	}

	// Create OAuth2Auth instance for this specific provider
	oauth2Auth, err := NewOAuth2Auth(
		lo.FromPtr(provider.Metadata.Name), // Provider name from metadata
		displayName,                        // Display name from spec or fallback to provider name
		lo.FromPtr(oauth2Spec.Issuer),
		oauth2Spec.AuthorizationUrl,
		oauth2Spec.TokenUrl,
		oauth2Spec.UserinfoUrl,
		oauth2Spec.ClientId,
		lo.FromPtr(oauth2Spec.ClientSecret),
		scopes,
		tlsConfig,
		orgConfig,
		usernameClaim,
		roleExtractor,
		log,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create OAuth2 auth for provider %s: %w",
			lo.FromPtr(provider.Metadata.Name), err)
	}

	return oauth2Auth, nil
}

// convertOrganizationAssignmentToOrgConfig converts auth organization assignment to org config
func convertOrganizationAssignmentToOrgConfig(assignment api.AuthOrganizationAssignment) *common.AuthOrganizationsConfig {
	orgConfig := &common.AuthOrganizationsConfig{
		Enabled: true,
	}

	// Get the discriminator to determine the assignment type
	discriminator, err := assignment.Discriminator()
	if err != nil {
		// If we can't determine the type, return a basic config
		return orgConfig
	}

	// Convert based on the assignment type
	switch discriminator {
	case OrganizationAssignmentTypeStatic:
		if staticAssignment, err := assignment.AsAuthStaticOrganizationAssignment(); err == nil {
			orgConfig.OrganizationAssignment = &common.OrganizationAssignment{
				Type:             OrganizationAssignmentTypeStatic,
				OrganizationName: &staticAssignment.OrganizationName,
			}
		}
	case OrganizationAssignmentTypeDynamic:
		if dynamicAssignment, err := assignment.AsAuthDynamicOrganizationAssignment(); err == nil {
			orgConfig.OrganizationAssignment = &common.OrganizationAssignment{
				Type:                   OrganizationAssignmentTypeDynamic,
				ClaimPath:              dynamicAssignment.ClaimPath,
				OrganizationNamePrefix: dynamicAssignment.OrganizationNamePrefix,
				OrganizationNameSuffix: dynamicAssignment.OrganizationNameSuffix,
			}
		}
	case OrganizationAssignmentTypePerUser:
		if perUserAssignment, err := assignment.AsAuthPerUserOrganizationAssignment(); err == nil {
			prefix := "user-org-"
			suffix := ""
			if perUserAssignment.OrganizationNamePrefix != nil {
				prefix = *perUserAssignment.OrganizationNamePrefix
			}
			if perUserAssignment.OrganizationNameSuffix != nil {
				suffix = *perUserAssignment.OrganizationNameSuffix
			}
			orgConfig.OrganizationAssignment = &common.OrganizationAssignment{
				Type:                   OrganizationAssignmentTypePerUser,
				OrganizationNamePrefix: &prefix,
				OrganizationNameSuffix: &suffix,
			}
		}
	}

	return orgConfig
}
