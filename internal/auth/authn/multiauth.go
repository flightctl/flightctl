package authn

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v2/jwt"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

// ParsedTokenCtxKey is the context key for the parsed JWT token
const ParsedTokenCtxKey common.ContextKey = "parsed_token"

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
	ListAuthProviders(ctx context.Context, orgId uuid.UUID, params api.ListAuthProvidersParams) (*api.AuthProviderList, api.Status)
	GetAuthProvider(ctx context.Context, orgId uuid.UUID, name string) (*api.AuthProvider, api.Status)
	GetAuthProviderByIssuerAndClientId(ctx context.Context, orgId uuid.UUID, issuer string, clientId string) (*api.AuthProvider, api.Status)
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
}

// NewMultiAuth creates a new MultiAuth instance
func NewMultiAuth(authProviderService AuthProviderService, tlsConfig *tls.Config, log logrus.FieldLogger) *MultiAuth {
	m := &MultiAuth{
		staticProviders:     make(map[string]common.AuthNMiddleware),
		authProviderService: authProviderService,
		tlsConfig:           tlsConfig,
		log:                 log,
		dynamicProviders:    make(map[AuthProviderCacheKey]common.AuthNMiddleware),
	}

	return m
}

// AddStaticProvider adds a static authentication provider with its issuer
func (m *MultiAuth) AddStaticProvider(issuer string, provider common.AuthNMiddleware) {
	m.staticProviders[issuer] = provider
}
func (m *MultiAuth) IsEnabled() bool {
	return true
}

// HasProviders returns true if any providers are configured
func (m *MultiAuth) HasProviders() bool {
	return len(m.staticProviders) > 0
}

// GetProviderMiddleware retrieves the provider middleware directly by name (for internal use with secrets intact)
func (m *MultiAuth) GetProviderMiddleware(name string) (common.AuthNMiddleware, api.Status) {
	// Check static providers by name
	for _, provider := range m.staticProviders {
		config := provider.GetAuthConfig()
		if config.Providers != nil {
			for _, prov := range *config.Providers {
				if prov.Metadata.Name != nil && *prov.Metadata.Name == name {
					return provider, api.StatusOK()
				}
			}
		}
	}

	// Check dynamic providers by name
	m.dynamicProvidersMu.RLock()
	defer m.dynamicProvidersMu.RUnlock()
	for _, provider := range m.dynamicProviders {
		config := provider.GetAuthConfig()
		if config.Providers != nil {
			for _, prov := range *config.Providers {
				if prov.Metadata.Name != nil && *prov.Metadata.Name == name {
					return provider, api.StatusOK()
				}
			}
		}
	}

	return nil, api.StatusResourceNotFound("AuthProvider", name)
}

// GetTLSConfig returns the TLS configuration
func (m *MultiAuth) GetTLSConfig() *tls.Config {
	return m.tlsConfig
}

// GetLogger returns the logger
func (m *MultiAuth) GetLogger() logrus.FieldLogger {
	return m.log
}

// Start starts the background loader goroutine and blocks until context is cancelled
func (m *MultiAuth) Start(ctx context.Context) {
	m.periodicLoader(ctx)
}

// periodicLoader runs in the background and reloads dynamic providers every 5 seconds
func (m *MultiAuth) periodicLoader(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// Load immediately on start
	if err := m.LoadAllAuthProviders(ctx); err != nil {
		m.log.Warnf("Failed to load auth providers on startup: %v", err)
	}

	for {
		select {
		case <-ticker.C:
			if err := m.LoadAllAuthProviders(ctx); err != nil {
				m.log.Warnf("Failed to reload auth providers: %v", err)
			}
		case <-ctx.Done():
			m.log.Info("Stopping auth provider loader")
			return
		}
	}
}

// LoadAllAuthProviders reloads auth providers from the database with change detection
func (m *MultiAuth) LoadAllAuthProviders(ctx context.Context) error {

	// List all auth providers from database
	providerList, status := m.authProviderService.ListAuthProviders(ctx, store.NullOrgId, api.ListAuthProvidersParams{})
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

	case string(api.Oauth2):
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

	existingProvider := (*existingConfig.Providers)[0]

	// Get existing provider discriminator
	existingDiscriminator, err := existingProvider.Spec.Discriminator()
	if err != nil {
		return true, err
	}

	// Determine new provider type
	newDiscriminator, err := newProvider.Spec.Discriminator()
	if err != nil {
		return true, err
	}

	// If types differ, provider has changed
	if existingDiscriminator != newDiscriminator {
		return true, nil
	}

	// Compare based on provider type
	switch newDiscriminator {
	case string(api.Oidc):
		existingOidcSpec, err := existingProvider.Spec.AsOIDCProviderSpec()
		if err != nil {
			return true, err
		}
		newOidcSpec, err := newProvider.Spec.AsOIDCProviderSpec()
		if err != nil {
			return true, err
		}

		// Compare relevant fields
		if existingOidcSpec.Issuer != newOidcSpec.Issuer {
			return true, nil
		}
		if existingOidcSpec.ClientId != newOidcSpec.ClientId {
			return true, nil
		}
		if (existingOidcSpec.DisplayName == nil) != (newOidcSpec.DisplayName == nil) {
			return true, nil
		}
		if existingOidcSpec.DisplayName != nil && newOidcSpec.DisplayName != nil && *existingOidcSpec.DisplayName != *newOidcSpec.DisplayName {
			return true, nil
		}
		if (existingOidcSpec.Enabled == nil) != (newOidcSpec.Enabled == nil) {
			return true, nil
		}
		if existingOidcSpec.Enabled != nil && newOidcSpec.Enabled != nil && *existingOidcSpec.Enabled != *newOidcSpec.Enabled {
			return true, nil
		}
		if !equalStringSlices(existingOidcSpec.UsernameClaim, newOidcSpec.UsernameClaim) {
			return true, nil
		}
		// Compare scopes
		if !equalScopes(existingOidcSpec.Scopes, newOidcSpec.Scopes) {
			return true, nil
		}

	case string(api.Oauth2):
		existingOauth2Spec, err := existingProvider.Spec.AsOAuth2ProviderSpec()
		if err != nil {
			return true, err
		}
		newOauth2Spec, err := newProvider.Spec.AsOAuth2ProviderSpec()
		if err != nil {
			return true, err
		}

		// Compare relevant fields
		if (existingOauth2Spec.Issuer == nil) != (newOauth2Spec.Issuer == nil) {
			return true, nil
		}
		if existingOauth2Spec.Issuer != nil && newOauth2Spec.Issuer != nil && *existingOauth2Spec.Issuer != *newOauth2Spec.Issuer {
			return true, nil
		}
		if existingOauth2Spec.AuthorizationUrl != newOauth2Spec.AuthorizationUrl {
			return true, nil
		}
		if existingOauth2Spec.TokenUrl != newOauth2Spec.TokenUrl {
			return true, nil
		}
		if existingOauth2Spec.UserinfoUrl != newOauth2Spec.UserinfoUrl {
			return true, nil
		}
		if existingOauth2Spec.ClientId != newOauth2Spec.ClientId {
			return true, nil
		}
		if (existingOauth2Spec.DisplayName == nil) != (newOauth2Spec.DisplayName == nil) {
			return true, nil
		}
		if existingOauth2Spec.DisplayName != nil && newOauth2Spec.DisplayName != nil && *existingOauth2Spec.DisplayName != *newOauth2Spec.DisplayName {
			return true, nil
		}
		if (existingOauth2Spec.Enabled == nil) != (newOauth2Spec.Enabled == nil) {
			return true, nil
		}
		if existingOauth2Spec.Enabled != nil && newOauth2Spec.Enabled != nil && *existingOauth2Spec.Enabled != *newOauth2Spec.Enabled {
			return true, nil
		}
		if !equalStringSlices(existingOauth2Spec.UsernameClaim, newOauth2Spec.UsernameClaim) {
			return true, nil
		}
		// Compare scopes
		if !equalScopes(existingOauth2Spec.Scopes, newOauth2Spec.Scopes) {
			return true, nil
		}

	default:
		return true, fmt.Errorf("unsupported provider type: %s", newDiscriminator)
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

	case string(api.Oauth2):
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
	allProviders := []api.AuthProvider{}
	var orgEnabled bool
	var firstStaticProviderName string

	// Collect static provider names and sort them for consistent ordering
	staticProviderNames := make([]string, 0, len(m.staticProviders))
	for name := range m.staticProviders {
		staticProviderNames = append(staticProviderNames, name)
	}
	sort.Strings(staticProviderNames)

	// Collect all static providers in sorted order
	for _, name := range staticProviderNames {
		provider := m.staticProviders[name]
		config := provider.GetAuthConfig()

		// Get org config from first provider config
		if config.OrganizationsEnabled != nil {
			orgEnabled = *config.OrganizationsEnabled
		}

		// Add all providers from this config (filter by enabled=true)
		if config.Providers != nil {
			for _, prov := range *config.Providers {
				if m.isProviderEnabled(&prov) {
					// Capture the first enabled static provider name
					if firstStaticProviderName == "" && prov.Metadata.Name != nil {
						firstStaticProviderName = *prov.Metadata.Name
					}
					allProviders = append(allProviders, prov)
				}
			}
		}
	}

	// Collect all dynamic providers (filter by enabled=true)
	m.dynamicProvidersMu.RLock()
	for _, provider := range m.dynamicProviders {
		config := provider.GetAuthConfig()

		// Add all enabled providers from this config
		if config.Providers != nil {
			for _, prov := range *config.Providers {
				if m.isProviderEnabled(&prov) {
					allProviders = append(allProviders, prov)
				}
			}
		}
	}
	m.dynamicProvidersMu.RUnlock()

	// Sort providers by name for consistent ordering
	sort.Slice(allProviders, func(i, j int) bool {
		nameI := ""
		nameJ := ""
		if allProviders[i].Metadata.Name != nil {
			nameI = *allProviders[i].Metadata.Name
		}
		if allProviders[j].Metadata.Name != nil {
			nameJ = *allProviders[j].Metadata.Name
		}
		return nameI < nameJ
	})

	// If no providers found, return config with nil default provider
	if len(allProviders) == 0 {
		return &api.AuthConfig{
			ApiVersion:           api.AuthConfigAPIVersion,
			DefaultProvider:      nil,
			OrganizationsEnabled: &orgEnabled,
			Providers:            &allProviders,
		}
	}

	// Set default provider to the first static provider
	defaultProviderName := firstStaticProviderName

	return &api.AuthConfig{
		ApiVersion:           api.AuthConfigAPIVersion,
		DefaultProvider:      &defaultProviderName,
		OrganizationsEnabled: &orgEnabled,
		Providers:            &allProviders,
	}
}

// isProviderEnabled checks if a provider has enabled=true
func (m *MultiAuth) isProviderEnabled(provider *api.AuthProvider) bool {
	// Check the provider spec's Enabled field based on provider type
	providerType, err := provider.Spec.Discriminator()
	if err != nil {
		return false
	}

	switch providerType {
	case string(api.Oidc):
		if spec, err := provider.Spec.AsOIDCProviderSpec(); err == nil {
			return spec.Enabled != nil && *spec.Enabled
		}
	case string(api.Oauth2):
		if spec, err := provider.Spec.AsOAuth2ProviderSpec(); err == nil {
			return spec.Enabled != nil && *spec.Enabled
		}
	case string(api.Openshift):
		if spec, err := provider.Spec.AsOpenShiftProviderSpec(); err == nil {
			return spec.Enabled != nil && *spec.Enabled
		}
	case string(api.Aap):
		if spec, err := provider.Spec.AsAapProviderSpec(); err == nil {
			return spec.Enabled != nil && *spec.Enabled
		}
	case string(api.K8s):
		if spec, err := provider.Spec.AsK8sProviderSpec(); err == nil {
			return spec.Enabled != nil && *spec.Enabled
		}
	}

	// If no Enabled field found or if provider type is unknown, don't include it
	return false
}

// getPossibleProviders extracts possible providers from a token
// Returns a list of providers and the parsed JWT token (nil if not a JWT)
func (m *MultiAuth) getPossibleProviders(token string) ([]common.AuthNMiddleware, jwt.Token, error) {
	// Try to parse as JWT token
	parsedToken, err := parseToken(token)
	if err != nil || parsedToken.Issuer() == "" {
		// Not a JWT token or JWT without issuer - return all possible enabled providers for opaque tokens
		providers := []common.AuthNMiddleware{}

		// Add all enabled static providers
		for _, provider := range m.staticProviders {
			if provider.IsEnabled() {
				providers = append(providers, provider)
			}
		}
		// Add all enabled cached dynamic providers
		m.dynamicProvidersMu.RLock()
		for _, provider := range m.dynamicProviders {
			if provider.IsEnabled() {
				providers = append(providers, provider)
			}
		}
		m.dynamicProvidersMu.RUnlock()

		return providers, nil, nil
	}

	// Detect token type
	tokenType := detectTokenType(parsedToken)

	switch tokenType {
	case TokenTypeK8s:
		// K8s tokens: use static "k8s" key

		providers := []common.AuthNMiddleware{}
		if provider, exists := m.staticProviders["k8s"]; exists {
			if provider.IsEnabled() {
				providers = append(providers, provider)
			}
		}
		for _, provider := range m.staticProviders {
			//check if the provider has the getOpenShiftSpec method
			if openshiftProvider, ok := provider.(*OpenShiftAuth); ok {
				if openshiftProvider.IsEnabled() {
					providers = append(providers, openshiftProvider)
				}
			}
		}
		if len(providers) == 0 {
			return []common.AuthNMiddleware{}, parsedToken, fmt.Errorf("no enabled K8s/openshift provider found")
		}
		return providers, parsedToken, nil

	case TokenTypeOIDC:
		// OIDC tokens: collect all enabled providers matching issuer+clientId
		issuer := parsedToken.Issuer()
		clientIds := parsedToken.Audience()
		if len(clientIds) == 0 {
			return []common.AuthNMiddleware{}, parsedToken, fmt.Errorf("OIDC token missing audience claim")
		}

		providers := []common.AuthNMiddleware{}
		// Try each client ID to find matching enabled providers
		for _, clientId := range clientIds {
			// 1. Check static config-based providers (using string key)
			staticKey := fmt.Sprintf("%s:%s", issuer, clientId)
			if provider, exists := m.staticProviders[staticKey]; exists {
				if provider.IsEnabled() {
					providers = append(providers, provider)
				}
			}

			// 2. Check cached dynamic auth providers (pre-loaded and synced via events)
			if provider, exists := m.getDynamicAuthProvider(issuer, clientId); exists {
				if provider.IsEnabled() {
					providers = append(providers, provider)
				}
			}
		}

		if len(providers) == 0 {
			return []common.AuthNMiddleware{}, parsedToken, fmt.Errorf("no enabled OIDC provider found for issuer: %s, clientIds: %v", issuer, clientIds)
		}

		return providers, parsedToken, nil

	default:
		return []common.AuthNMiddleware{}, parsedToken, fmt.Errorf("unknown token type")
	}
}

// detectTokenType determines the type of JWT token based on its claims
func detectTokenType(parsedToken jwt.Token) TokenType {
	// Check for K8s tokens by looking for the kubernetes.io claim
	if _, ok := parsedToken.Get("kubernetes.io"); ok {
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
	case string(api.Oauth2):
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

	// Create OIDCAuth instance for this specific provider
	oidcAuth, err := NewOIDCAuth(provider.Metadata, oidcSpec, tlsConfig)
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

	// Create OAuth2Auth instance for this specific provider
	oauth2Auth, err := NewOAuth2Auth(provider.Metadata, oauth2Spec, tlsConfig, log)
	if err != nil {
		return nil, fmt.Errorf("failed to create OAuth2 auth for provider %s: %w",
			lo.FromPtr(provider.Metadata.Name), err)
	}

	return oauth2Auth, nil
}

// convertOrganizationAssignmentToOrgConfig converts auth organization assignment to org config
func convertOrganizationAssignmentToOrgConfig(assignment api.AuthOrganizationAssignment) *common.AuthOrganizationsConfig {
	return &common.AuthOrganizationsConfig{
		Enabled:                true,
		OrganizationAssignment: &assignment,
	}
}
