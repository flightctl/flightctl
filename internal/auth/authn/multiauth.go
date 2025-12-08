package authn

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"sort"
	"sync"
	"time"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/auth/common"
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
	ListAllAuthProviders(ctx context.Context, params api.ListAuthProvidersParams) (*api.AuthProviderList, api.Status)
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

	// Start protection
	startMu sync.Mutex
	started bool
}

// AuthProviderWithLifecycle is an optional interface that providers can implement
// if they need lifecycle management (e.g., starting background caches)
type AuthProviderWithLifecycle interface {
	common.AuthNMiddleware
	Start(ctx context.Context) error
	Stop()
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
func (m *MultiAuth) Start(ctx context.Context) error {
	m.startMu.Lock()
	defer m.startMu.Unlock()

	if m.started {
		return fmt.Errorf("MultiAuth provider already started")
	}

	for _, provider := range m.staticProviders {
		if lifecycleProvider, ok := provider.(AuthProviderWithLifecycle); ok {
			if err := lifecycleProvider.Start(ctx); err != nil {
				return fmt.Errorf("failed to start static provider: %w", err)
			}
		}
	}

	m.started = true

	// Dynamic providers (loaded from DB) get their own cancellable contexts in LoadAllAuthProviders
	// so they can be independently reloaded/removed without affecting others

	m.periodicLoader(ctx)
	return nil
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

	// List all auth providers from database without org filtering
	providerList, status := m.authProviderService.ListAllAuthProviders(ctx, api.ListAuthProvidersParams{})
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
			changed, err := m.hasProviderChanged(existingMiddleware, provider, lo.FromPtr(provider.Metadata.Name))
			if err != nil {
				m.log.Warnf("Failed to check if provider %s changed: %v", lo.FromPtr(provider.Metadata.Name), err)
				continue
			}

			if changed {
				// Provider changed - stop old provider first
				if lifecycleProvider, ok := existingMiddleware.(AuthProviderWithLifecycle); ok {
					lifecycleProvider.Stop()
				}

				// Reconstruct middleware
				_, authMiddleware, err := m.createAuthMiddlewareFromProvider(ctx, provider)
				if err != nil {
					m.log.Warnf("Failed to update auth provider %s: %v", lo.FromPtr(provider.Metadata.Name), err)
					continue
				}

				// Start the provider if it implements lifecycle management
				if lifecycleProvider, ok := authMiddleware.(AuthProviderWithLifecycle); ok {
					if err := lifecycleProvider.Start(ctx); err != nil {
						m.log.Warnf("Failed to start updated auth provider %s: %v", lo.FromPtr(provider.Metadata.Name), err)
						continue
					}
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
			_, authMiddleware, err := m.createAuthMiddlewareFromProvider(ctx, provider)
			if err != nil {
				m.log.Warnf("Failed to create auth provider %s: %v", lo.FromPtr(provider.Metadata.Name), err)
				continue
			}

			// Start the provider if it implements lifecycle management
			if lifecycleProvider, ok := authMiddleware.(AuthProviderWithLifecycle); ok {
				if err := lifecycleProvider.Start(ctx); err != nil {
					m.log.Warnf("Failed to start new auth provider %s: %v", lo.FromPtr(provider.Metadata.Name), err)
					continue
				}
			}

			m.dynamicProviders[providerKey] = authMiddleware
			m.log.Infof("Added new auth provider: %s", lo.FromPtr(provider.Metadata.Name))
			addedCount++
		}
	}

	// Remove providers that are no longer in DB
	removedCount := 0
	for providerKey, provider := range m.dynamicProviders {
		if !processedKeys[providerKey] {
			// Stop the provider to clean up its resources
			if lifecycleProvider, ok := provider.(AuthProviderWithLifecycle); ok {
				lifecycleProvider.Stop()
			}
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
func (m *MultiAuth) hasProviderChanged(existingMiddleware common.AuthNMiddleware, newProvider *api.AuthProvider, providerName string) (bool, error) {
	// Determine new provider type
	newDiscriminator, err := newProvider.Spec.Discriminator()
	if err != nil {
		m.log.Debugf("Provider %s: changed (failed to get new discriminator: %v)", providerName, err)
		return true, err
	}

	// Compare based on provider type using GetXXXSpec methods to access full specs including secrets
	switch newDiscriminator {
	case string(api.Oidc):
		// Get existing OIDC spec from middleware
		existingOidcProvider, ok := existingMiddleware.(interface{ GetOIDCSpec() api.OIDCProviderSpec })
		if !ok {
			return true, nil // Middleware doesn't support OIDC, assume changed
		}
		existingOidcSpec := existingOidcProvider.GetOIDCSpec()

		newOidcSpec, err := newProvider.Spec.AsOIDCProviderSpec()
		if err != nil {
			return true, err
		}

		// Compare all fields including client secret
		if existingOidcSpec.Issuer != newOidcSpec.Issuer {
			m.log.Debugf("Provider %s: changed (OIDC Issuer: existing=%q, new=%q)", providerName, existingOidcSpec.Issuer, newOidcSpec.Issuer)
			return true, nil
		}
		if existingOidcSpec.ClientId != newOidcSpec.ClientId {
			m.log.Debugf("Provider %s: changed (OIDC ClientId: existing=%q, new=%q)", providerName, existingOidcSpec.ClientId, newOidcSpec.ClientId)
			return true, nil
		}
		if (existingOidcSpec.ClientSecret == nil) != (newOidcSpec.ClientSecret == nil) {
			m.log.Debugf("Provider %s: changed (OIDC ClientSecret)", providerName)
			return true, nil
		}
		if existingOidcSpec.ClientSecret != nil && newOidcSpec.ClientSecret != nil && *existingOidcSpec.ClientSecret != *newOidcSpec.ClientSecret {
			m.log.Debugf("Provider %s: changed (OIDC ClientSecret)", providerName)
			return true, nil
		}
		if existingOidcSpec.ProviderType != newOidcSpec.ProviderType {
			m.log.Debugf("Provider %s: changed (OIDC ProviderType: existing=%q, new=%q)", providerName, existingOidcSpec.ProviderType, newOidcSpec.ProviderType)
			return true, nil
		}
		if (existingOidcSpec.DisplayName == nil) != (newOidcSpec.DisplayName == nil) {
			m.log.Debugf("Provider %s: changed (OIDC DisplayName: existing=%q, new=%q)", providerName, existingOidcSpec.DisplayName, newOidcSpec.DisplayName)
			return true, nil
		}
		// Normalize DisplayName comparison: treat nil and empty string as equivalent
		existingDisplayName := lo.FromPtr(existingOidcSpec.DisplayName)
		newDisplayName := lo.FromPtr(newOidcSpec.DisplayName)
		if existingDisplayName != newDisplayName {
			m.log.Debugf("Provider %s: changed (OIDC DisplayName: existing=%q, new=%q)", providerName, existingDisplayName, newDisplayName)
			return true, nil
		}
		if (existingOidcSpec.Enabled == nil) != (newOidcSpec.Enabled == nil) {
			m.log.Debugf("Provider %s: changed (OIDC Enabled nil mismatch: existing=%v, new=%v)", providerName, existingOidcSpec.Enabled == nil, newOidcSpec.Enabled == nil)
			return true, nil
		}
		if existingOidcSpec.Enabled != nil && newOidcSpec.Enabled != nil && *existingOidcSpec.Enabled != *newOidcSpec.Enabled {
			m.log.Debugf("Provider %s: changed (OIDC Enabled: existing=%v, new=%v)", providerName, *existingOidcSpec.Enabled, *newOidcSpec.Enabled)
			return true, nil
		}
		// Compare UsernameClaim directly (defaults should be set in validation if needed)
		if !equalStringSlices(existingOidcSpec.UsernameClaim, newOidcSpec.UsernameClaim) {
			m.log.Debugf("Provider %s: changed (OIDC UsernameClaim: existing=%v, new=%v)", providerName, existingOidcSpec.UsernameClaim, newOidcSpec.UsernameClaim)
			return true, nil
		}
		// Compare scopes
		if !equalScopes(existingOidcSpec.Scopes, newOidcSpec.Scopes) {
			m.log.Debugf("Provider %s: changed (OIDC Scopes: existing=%v, new=%v)", providerName, existingOidcSpec.Scopes, newOidcSpec.Scopes)
			return true, nil
		}
		// Compare organization assignment
		if !equalOrganizationAssignments(existingOidcSpec.OrganizationAssignment, newOidcSpec.OrganizationAssignment) {
			m.log.Debugf("Provider %s: changed (OIDC OrganizationAssignment)", providerName)
			return true, nil
		}
		// Compare role assignment
		if !equalRoleAssignments(existingOidcSpec.RoleAssignment, newOidcSpec.RoleAssignment) {
			m.log.Debugf("Provider %s: changed (OIDC RoleAssignment)", providerName)
			return true, nil
		}

	case string(api.Oauth2):
		// Get existing OAuth2 spec from middleware
		existingOauth2Provider, ok := existingMiddleware.(interface{ GetOAuth2Spec() api.OAuth2ProviderSpec })
		if !ok {
			return true, nil // Middleware doesn't support OAuth2, assume changed
		}
		existingOauth2Spec := existingOauth2Provider.GetOAuth2Spec()

		newOauth2Spec, err := newProvider.Spec.AsOAuth2ProviderSpec()
		if err != nil {
			return true, err
		}

		// Compare all fields including client secret
		if (existingOauth2Spec.Issuer == nil) != (newOauth2Spec.Issuer == nil) {
			return true, nil
		}
		if existingOauth2Spec.Issuer != nil && newOauth2Spec.Issuer != nil && *existingOauth2Spec.Issuer != *newOauth2Spec.Issuer {
			return true, nil
		}
		if existingOauth2Spec.AuthorizationUrl != newOauth2Spec.AuthorizationUrl {
			m.log.Debugf("Provider %s: changed (OAuth2 AuthorizationUrl: existing=%q, new=%q)", providerName, existingOauth2Spec.AuthorizationUrl, newOauth2Spec.AuthorizationUrl)
			return true, nil
		}
		if existingOauth2Spec.TokenUrl != newOauth2Spec.TokenUrl {
			m.log.Debugf("Provider %s: changed (OAuth2 TokenUrl: existing=%q, new=%q)", providerName, existingOauth2Spec.TokenUrl, newOauth2Spec.TokenUrl)
			return true, nil
		}
		if existingOauth2Spec.UserinfoUrl != newOauth2Spec.UserinfoUrl {
			m.log.Debugf("Provider %s: changed (OAuth2 UserinfoUrl: existing=%q, new=%q)", providerName, existingOauth2Spec.UserinfoUrl, newOauth2Spec.UserinfoUrl)
			return true, nil
		}
		if existingOauth2Spec.ClientId != newOauth2Spec.ClientId {
			m.log.Debugf("Provider %s: changed (OAuth2 ClientId: existing=%q, new=%q)", providerName, existingOauth2Spec.ClientId, newOauth2Spec.ClientId)
			return true, nil
		}
		if (existingOauth2Spec.ClientSecret == nil) != (newOauth2Spec.ClientSecret == nil) {
			return true, nil
		}
		if existingOauth2Spec.ClientSecret != nil && newOauth2Spec.ClientSecret != nil && *existingOauth2Spec.ClientSecret != *newOauth2Spec.ClientSecret {
			return true, nil
		}
		if existingOauth2Spec.ProviderType != newOauth2Spec.ProviderType {
			return true, nil
		}
		if (existingOauth2Spec.DisplayName == nil) != (newOauth2Spec.DisplayName == nil) {
			return true, nil
		}
		if existingOauth2Spec.DisplayName != nil && newOauth2Spec.DisplayName != nil && *existingOauth2Spec.DisplayName != *newOauth2Spec.DisplayName {
			return true, nil
		}
		if (existingOauth2Spec.Enabled == nil) != (newOauth2Spec.Enabled == nil) {
			m.log.Debugf("Provider %s: changed (OAuth2 Enabled nil mismatch: existing=%v, new=%v)", providerName, existingOauth2Spec.Enabled == nil, newOauth2Spec.Enabled == nil)
			return true, nil
		}
		if existingOauth2Spec.Enabled != nil && newOauth2Spec.Enabled != nil && *existingOauth2Spec.Enabled != *newOauth2Spec.Enabled {
			m.log.Debugf("Provider %s: changed (OAuth2 Enabled: existing=%v, new=%v)", providerName, *existingOauth2Spec.Enabled, *newOauth2Spec.Enabled)
			return true, nil
		}
		// Compare UsernameClaim directly (defaults are set in validation.go)
		if !equalStringSlices(existingOauth2Spec.UsernameClaim, newOauth2Spec.UsernameClaim) {
			m.log.Debugf("Provider %s: changed (OAuth2 UsernameClaim: existing=%v, new=%v)", providerName, existingOauth2Spec.UsernameClaim, newOauth2Spec.UsernameClaim)
			return true, nil
		}
		// Compare scopes
		if !equalScopes(existingOauth2Spec.Scopes, newOauth2Spec.Scopes) {
			m.log.Debugf("Provider %s: changed (OAuth2 Scopes: existing=%v, new=%v)", providerName, existingOauth2Spec.Scopes, newOauth2Spec.Scopes)
			return true, nil
		}
		// Compare introspection
		if !equalOAuth2Introspection(existingOauth2Spec.Introspection, newOauth2Spec.Introspection) {
			return true, nil
		}
		// Compare organization assignment
		if !equalOrganizationAssignments(existingOauth2Spec.OrganizationAssignment, newOauth2Spec.OrganizationAssignment) {
			m.log.Debugf("Provider %s: changed (OAuth2 OrganizationAssignment)", providerName)
			return true, nil
		}
		// Compare role assignment
		if !equalRoleAssignments(existingOauth2Spec.RoleAssignment, newOauth2Spec.RoleAssignment) {
			m.log.Debugf("Provider %s: changed (OAuth2 RoleAssignment)", providerName)
			return true, nil
		}

	default:
		return true, fmt.Errorf("unsupported provider type: %s", newDiscriminator)
	}

	return false, nil
}

// equalOAuth2Introspection compares two OAuth2Introspection values
func equalOAuth2Introspection(a, b *api.OAuth2Introspection) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	// Use reflect.DeepEqual for union type comparison
	return reflect.DeepEqual(a, b)
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

// equalOrganizationAssignments compares two AuthOrganizationAssignment configurations
func equalOrganizationAssignments(a, b api.AuthOrganizationAssignment) bool {
	aDiscriminator, err := a.Discriminator()
	if err != nil {
		return false
	}
	bDiscriminator, err := b.Discriminator()
	if err != nil {
		return false
	}

	if aDiscriminator != bDiscriminator {
		return false
	}

	switch aDiscriminator {
	case string(api.AuthStaticOrganizationAssignmentTypeStatic):
		aSpec, err := a.AsAuthStaticOrganizationAssignment()
		if err != nil {
			return false
		}
		bSpec, err := b.AsAuthStaticOrganizationAssignment()
		if err != nil {
			return false
		}
		return aSpec.OrganizationName == bSpec.OrganizationName

	case string(api.AuthDynamicOrganizationAssignmentTypeDynamic):
		aSpec, err := a.AsAuthDynamicOrganizationAssignment()
		if err != nil {
			return false
		}
		bSpec, err := b.AsAuthDynamicOrganizationAssignment()
		if err != nil {
			return false
		}
		if !slices.Equal(aSpec.ClaimPath, bSpec.ClaimPath) {
			return false
		}
		if (aSpec.OrganizationNamePrefix == nil) != (bSpec.OrganizationNamePrefix == nil) {
			return false
		}
		if aSpec.OrganizationNamePrefix != nil && bSpec.OrganizationNamePrefix != nil && *aSpec.OrganizationNamePrefix != *bSpec.OrganizationNamePrefix {
			return false
		}
		if (aSpec.OrganizationNameSuffix == nil) != (bSpec.OrganizationNameSuffix == nil) {
			return false
		}
		if aSpec.OrganizationNameSuffix != nil && bSpec.OrganizationNameSuffix != nil && *aSpec.OrganizationNameSuffix != *bSpec.OrganizationNameSuffix {
			return false
		}
		return true

	case string(api.PerUser):
		aSpec, err := a.AsAuthPerUserOrganizationAssignment()
		if err != nil {
			return false
		}
		bSpec, err := b.AsAuthPerUserOrganizationAssignment()
		if err != nil {
			return false
		}
		if (aSpec.OrganizationNamePrefix == nil) != (bSpec.OrganizationNamePrefix == nil) {
			return false
		}
		if aSpec.OrganizationNamePrefix != nil && bSpec.OrganizationNamePrefix != nil && *aSpec.OrganizationNamePrefix != *bSpec.OrganizationNamePrefix {
			return false
		}
		if (aSpec.OrganizationNameSuffix == nil) != (bSpec.OrganizationNameSuffix == nil) {
			return false
		}
		if aSpec.OrganizationNameSuffix != nil && bSpec.OrganizationNameSuffix != nil && *aSpec.OrganizationNameSuffix != *bSpec.OrganizationNameSuffix {
			return false
		}
		return true

	default:
		return false
	}
}

// equalRoleAssignments compares two AuthRoleAssignment configurations
func equalRoleAssignments(a, b api.AuthRoleAssignment) bool {
	aDiscriminator, err := a.Discriminator()
	if err != nil {
		return false
	}
	bDiscriminator, err := b.Discriminator()
	if err != nil {
		return false
	}

	if aDiscriminator != bDiscriminator {
		return false
	}

	switch aDiscriminator {
	case string(api.AuthStaticRoleAssignmentTypeStatic):
		aSpec, err := a.AsAuthStaticRoleAssignment()
		if err != nil {
			return false
		}
		bSpec, err := b.AsAuthStaticRoleAssignment()
		if err != nil {
			return false
		}
		return slices.Equal(aSpec.Roles, bSpec.Roles)

	case string(api.AuthDynamicRoleAssignmentTypeDynamic):
		aSpec, err := a.AsAuthDynamicRoleAssignment()
		if err != nil {
			return false
		}
		bSpec, err := b.AsAuthDynamicRoleAssignment()
		if err != nil {
			return false
		}
		if !slices.Equal(aSpec.ClaimPath, bSpec.ClaimPath) {
			return false
		}
		if (aSpec.Separator == nil) != (bSpec.Separator == nil) {
			return false
		}
		if aSpec.Separator != nil && bSpec.Separator != nil && *aSpec.Separator != *bSpec.Separator {
			return false
		}
		return true

	default:
		return false
	}
}

// createAuthMiddlewareFromProvider creates an auth middleware from a provider and returns the cache key
func (m *MultiAuth) createAuthMiddlewareFromProvider(ctx context.Context, provider *api.AuthProvider) (AuthProviderCacheKey, common.AuthNMiddleware, error) {
	// Determine provider type
	discriminator, err := provider.Spec.Discriminator()
	if err != nil {
		return AuthProviderCacheKey{}, nil, fmt.Errorf("failed to determine provider type: %w", err)
	}

	// Create the auth middleware
	method, err := createAuthFromProvider(ctx, provider, m.tlsConfig, m.log)
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
func (m *MultiAuth) ValidateToken(ctx context.Context, token string) error {
	_, err := m.ValidateTokenAndGetProvider(ctx, token)
	if err != nil {
		return err
	}
	return nil
}

// ValidateTokenAndGetProvider validates a token using issuer-based routing and returns the provider that validated the token
func (m *MultiAuth) ValidateTokenAndGetProvider(ctx context.Context, token string) (common.AuthNMiddleware, error) {
	// Get possible providers for this token
	providers, parsedToken, err := m.getPossibleProviders(token)
	if err != nil {
		return nil, err
	}

	m.log.Debugf("MultiAuth: Attempting token validation with %d possible provider(s)", len(providers))

	// Try each provider until one validates successfully
	// Create a fresh context for each provider to avoid context cancellation propagation
	for i, provider := range providers {
		// Check if parent context is already done
		if ctx.Err() != nil {
			m.log.Warnf("MultiAuth: Parent context already canceled before trying provider %d: %v", i+1, ctx.Err())
			return nil, fmt.Errorf("parent context: %w", ctx.Err())
		}

		m.log.Debugf("MultiAuth: Trying provider %d/%d for token validation", i+1, len(providers))

		// Create a fresh context with timeout for this provider attempt
		// This prevents a failed/slow provider from affecting subsequent providers
		// Use 10 second timeout per provider to fail fast when token format doesn't match
		providerCtx, cancel := context.WithTimeout(ctx, 10*time.Second)

		// Add parsed token to the new context if it exists
		if parsedToken != nil {
			providerCtx = context.WithValue(providerCtx, ParsedTokenCtxKey, parsedToken)
		}

		err := provider.ValidateToken(providerCtx, token)
		cancel() // Always cancel to release resources

		if err == nil {
			m.log.Debugf("MultiAuth: Provider %d/%d validated token successfully", i+1, len(providers))
			return provider, nil
		}
		m.log.Debugf("MultiAuth: Provider %d/%d failed to validate token: %v", i+1, len(providers), err)
	}

	return nil, fmt.Errorf("token validation failed against all providers")
}

// GetIdentity extracts identity from a token using issuer-based routing
func (m *MultiAuth) GetIdentity(ctx context.Context, token string) (common.Identity, error) {
	// Get possible providers for this token
	providers, parsedToken, err := m.getPossibleProviders(token)
	if err != nil {
		return nil, err
	}

	m.log.Debugf("MultiAuth: Attempting to get identity with %d possible provider(s)", len(providers))

	// Note: We don't check parent context cancellation here because:
	// 1. The token was already validated successfully (this is called after ValidateToken)
	// 2. We need to get identity even if the request took longer than expected
	// 3. Each provider gets a fresh context with its own timeout anyway

	// Try each provider until one returns identity successfully
	// Create a fresh context for each provider to avoid context cancellation propagation
	for i, provider := range providers {
		m.log.Debugf("MultiAuth: Trying provider %d/%d to get identity", i+1, len(providers))

		// Create a fresh context with timeout for this provider attempt
		// This prevents a failed/slow provider from affecting subsequent providers
		// Use 10 second timeout per provider to fail fast when token format doesn't match
		providerCtx, cancel := context.WithTimeout(ctx, 10*time.Second)

		// Add parsed token to the new context if it exists
		if parsedToken != nil {
			providerCtx = context.WithValue(providerCtx, ParsedTokenCtxKey, parsedToken)
		}

		identity, err := provider.GetIdentity(providerCtx, token)
		cancel() // Always cancel to release resources

		if err == nil {
			m.log.Debugf("MultiAuth: Provider %d/%d returned identity successfully", i+1, len(providers))
			return identity, nil
		}
		m.log.Debugf("MultiAuth: Provider %d/%d failed to get identity: %v", i+1, len(providers), err)
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
	orgEnabled := true // Organizations are always enabled
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
func createAuthFromProvider(ctx context.Context, provider *api.AuthProvider, tlsConfig *tls.Config, log logrus.FieldLogger) (common.AuthNMiddleware, error) {
	// Get the discriminator to determine the provider type
	discriminator, err := provider.Spec.Discriminator()
	if err != nil {
		return nil, fmt.Errorf("failed to determine provider type: %w", err)
	}

	switch discriminator {
	case string(api.Oidc):
		return createOIDCAuthFromProvider(provider, tlsConfig, log)
	case string(api.Oauth2):
		return createOAuth2AuthFromProvider(ctx, provider, tlsConfig, log)
	default:
		return nil, fmt.Errorf("unsupported provider type: %s", discriminator)
	}
}

// createOIDCAuthFromProvider creates an OIDCAuth instance from an OIDC provider
func createOIDCAuthFromProvider(provider *api.AuthProvider, tlsConfig *tls.Config, log logrus.FieldLogger) (common.AuthNMiddleware, error) {
	oidcSpec, err := provider.Spec.AsOIDCProviderSpec()
	if err != nil {
		return nil, fmt.Errorf("failed to parse OIDC provider spec: %w", err)
	}

	// Create OIDCAuth instance for this specific provider
	oidcAuth, err := NewOIDCAuth(provider.Metadata, oidcSpec, tlsConfig, log)
	if err != nil {
		return nil, fmt.Errorf("failed to create OIDC auth for provider %s: %w",
			lo.FromPtr(provider.Metadata.Name), err)
	}

	return oidcAuth, nil
}

// createOAuth2AuthFromProvider creates an OAuth2Auth instance from an OAuth2 provider
func createOAuth2AuthFromProvider(ctx context.Context, provider *api.AuthProvider, tlsConfig *tls.Config, log logrus.FieldLogger) (common.AuthNMiddleware, error) {
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

	// Note: Start() will be called by the caller (LoadAllAuthProviders) if the provider implements AuthProviderWithLifecycle
	// This allows dynamic providers to have their lifecycle managed properly with cancellable contexts

	return oauth2Auth, nil
}

// convertOrganizationAssignmentToOrgConfig converts auth organization assignment to org config
func convertOrganizationAssignmentToOrgConfig(assignment api.AuthOrganizationAssignment) *common.AuthOrganizationsConfig {
	return &common.AuthOrganizationsConfig{
		OrganizationAssignment: &assignment,
	}
}
