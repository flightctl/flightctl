package common

import (
	"fmt"
	"slices"
	"time"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/org"
	"github.com/flightctl/flightctl/internal/util"
)

// AuthConfig holds authentication provider configurations.
type AuthConfig struct {
	K8s                     *api.K8sProviderSpec       `json:"k8s,omitempty"`
	OpenShift               *api.OpenShiftProviderSpec `json:"openshift,omitempty"`
	OIDC                    *api.OIDCProviderSpec      `json:"oidc,omitempty"`
	OAuth2                  *api.OAuth2ProviderSpec    `json:"oauth2,omitempty"`
	AAP                     *api.AapProviderSpec       `json:"aap,omitempty"`
	CACert                  string                     `json:"caCert,omitempty"`
	InsecureSkipTlsVerify   bool                       `json:"insecureSkipTlsVerify,omitempty"`
	DynamicProviderCacheTTL util.Duration              `json:"dynamicProviderCacheTTL,omitempty"`
}

// NewDefaultAuth returns a default auth configuration.
func NewDefaultAuth() *AuthConfig {
	return &AuthConfig{
		DynamicProviderCacheTTL: util.Duration(5 * time.Second),
	}
}

// NewAuthWithOIDC creates an auth config with OIDC authentication.
func NewAuthWithOIDC(issuer, clientId string, enabled bool) *AuthConfig {
	return &AuthConfig{
		DynamicProviderCacheTTL: util.Duration(5 * time.Second),
		OIDC: &api.OIDCProviderSpec{
			Issuer:       issuer,
			ClientId:     clientId,
			Enabled:      &enabled,
			ProviderType: api.Oidc,
		},
	}
}

// NewAuthWithAAP creates an auth config with AAP authentication.
func NewAuthWithAAP(apiUrl string, enabled bool) *AuthConfig {
	return &AuthConfig{
		DynamicProviderCacheTTL: util.Duration(5 * time.Second),
		AAP: &api.AapProviderSpec{
			ApiUrl:       apiUrl,
			ProviderType: api.Aap,
			Enabled:      &enabled,
		},
	}
}

// WithOIDC adds OIDC authentication to an existing auth config.
func (c *AuthConfig) WithOIDC(issuer, clientId string, enabled bool) *AuthConfig {
	c.OIDC = &api.OIDCProviderSpec{
		Issuer:       issuer,
		ClientId:     clientId,
		Enabled:      &enabled,
		ProviderType: api.Oidc,
	}
	return c
}

// WithAAP adds AAP authentication to an existing auth config.
func (c *AuthConfig) WithAAP(apiUrl string, enabled bool) *AuthConfig {
	c.AAP = &api.AapProviderSpec{
		ApiUrl:       apiUrl,
		ProviderType: api.Aap,
		Enabled:      &enabled,
	}
	return c
}

// ApplyDefaults applies default values to the auth config.
func (c *AuthConfig) ApplyDefaults(baseUrl, baseUIUrl string) error {
	if c == nil {
		return nil
	}

	c.applyProviderEnabledDefaults()
	c.applyOIDCClientDefaults(baseUrl)
	c.applyOpenShiftDefaults()
	return c.applyOAuth2Defaults()
}

func (c *AuthConfig) applyProviderEnabledDefaults() {
	if c.OIDC != nil && c.OIDC.Enabled == nil {
		enabled := true
		c.OIDC.Enabled = &enabled
	}
	if c.OpenShift != nil && c.OpenShift.Enabled == nil {
		enabled := true
		c.OpenShift.Enabled = &enabled
	}
	if c.K8s != nil && c.K8s.Enabled == nil {
		enabled := true
		c.K8s.Enabled = &enabled
	}
	if c.OAuth2 != nil && c.OAuth2.Enabled == nil {
		enabled := true
		c.OAuth2.Enabled = &enabled
	}
	if c.AAP != nil && c.AAP.Enabled == nil {
		enabled := true
		c.AAP.Enabled = &enabled
	}
}

func (c *AuthConfig) applyOIDCClientDefaults(baseUrl string) {
	if c.OIDC == nil {
		return
	}

	if c.OIDC.ClientId == "" {
		c.OIDC.ClientId = "flightctl-client"
	}
	if c.OIDC.Issuer == "" {
		c.OIDC.Issuer = baseUrl
	}
	if c.OIDC.UsernameClaim == nil {
		c.OIDC.UsernameClaim = &[]string{"preferred_username"}
	}

	c.applyOIDCRoleAssignmentDefaults()
	c.applyOIDCOrganizationAssignmentDefaults()
}

func (c *AuthConfig) applyOIDCRoleAssignmentDefaults() {
	if _, err := c.OIDC.RoleAssignment.Discriminator(); err != nil {
		dynamicRoleAssignment := api.AuthDynamicRoleAssignment{
			Type:      api.AuthDynamicRoleAssignmentTypeDynamic,
			ClaimPath: []string{"groups"},
		}
		_ = c.OIDC.RoleAssignment.FromAuthDynamicRoleAssignment(dynamicRoleAssignment)
	}
}

func (c *AuthConfig) applyOIDCOrganizationAssignmentDefaults() {
	if _, err := c.OIDC.OrganizationAssignment.Discriminator(); err != nil {
		staticAssignment := api.AuthStaticOrganizationAssignment{
			OrganizationName: org.DefaultExternalID,
			Type:             api.AuthStaticOrganizationAssignmentTypeStatic,
		}
		_ = c.OIDC.OrganizationAssignment.FromAuthStaticOrganizationAssignment(staticAssignment)
	}
}

func (c *AuthConfig) applyOpenShiftDefaults() {
	if c.OpenShift == nil {
		return
	}

	// Use authorizationUrl as issuer if issuer is not provided
	if c.OpenShift.Issuer == nil || *c.OpenShift.Issuer == "" {
		if c.OpenShift.AuthorizationUrl != nil {
			c.OpenShift.Issuer = c.OpenShift.AuthorizationUrl
		}
	}
}

func (c *AuthConfig) applyOAuth2Defaults() error {
	if c.OAuth2 == nil {
		return nil
	}

	// Infer introspection configuration if not provided
	if c.OAuth2.Introspection == nil {
		introspection, err := api.InferOAuth2IntrospectionConfig(*c.OAuth2)
		if err != nil {
			return fmt.Errorf("failed to infer OAuth2 introspection configuration: %w", err)
		}
		c.OAuth2.Introspection = introspection
	}
	return nil
}

// Validate validates the auth config.
func (c *AuthConfig) Validate() error {
	if c == nil {
		return nil
	}

	if c.OIDC != nil {
		if err := validateAuthProviderRoleAssignment(c.OIDC.RoleAssignment, string(api.Oidc)); err != nil {
			return err
		}
	}
	if c.OAuth2 != nil {
		if err := validateAuthProviderRoleAssignment(c.OAuth2.RoleAssignment, string(api.Oauth2)); err != nil {
			return err
		}
	}

	return nil
}

func validateAuthProviderRoleAssignment(roleAssignment api.AuthRoleAssignment, providerType string) error {
	discriminator, err := roleAssignment.Discriminator()
	if err != nil {
		// No role assignment configured, which is valid
		return nil
	}

	if discriminator != string(api.AuthStaticRoleAssignmentTypeStatic) {
		// Only validate static role assignments
		return nil
	}

	staticAssignment, err := roleAssignment.AsAuthStaticRoleAssignment()
	if err != nil {
		return fmt.Errorf("%s provider: invalid static role assignment: %w", providerType, err)
	}

	// Validate that all roles are in KnownExternalRoles
	for i, role := range staticAssignment.Roles {
		if role == "" {
			return fmt.Errorf("%s provider: role at index %d cannot be empty", providerType, i)
		}
		if !slices.Contains(api.KnownExternalRoles, role) {
			return fmt.Errorf("%s provider: role at index %d is not a valid role: %s (must be one of: %v)", providerType, i, role, api.KnownExternalRoles)
		}
	}

	return nil
}

// SanitizeForLogging redacts sensitive fields from the auth config.
func (c *AuthConfig) SanitizeForLogging() *AuthConfig {
	if c == nil {
		return nil
	}

	// Create a shallow copy
	sanitized := *c

	if sanitized.OIDC != nil {
		oidcCopy := *sanitized.OIDC
		if oidcCopy.ClientSecret != nil {
			redacted := "[REDACTED]"
			oidcCopy.ClientSecret = &redacted
		}
		sanitized.OIDC = &oidcCopy
	}
	if sanitized.OAuth2 != nil {
		oauth2Copy := *sanitized.OAuth2
		if oauth2Copy.ClientSecret != nil {
			redacted := "[REDACTED]"
			oauth2Copy.ClientSecret = &redacted
		}
		sanitized.OAuth2 = &oauth2Copy
	}
	if sanitized.OpenShift != nil {
		openShiftCopy := *sanitized.OpenShift
		if openShiftCopy.ClientSecret != nil {
			redacted := "[REDACTED]"
			openShiftCopy.ClientSecret = &redacted
		}
		sanitized.OpenShift = &openShiftCopy
	}
	if sanitized.AAP != nil {
		aapCopy := *sanitized.AAP
		if aapCopy.ClientSecret != nil {
			redacted := "[REDACTED]"
			aapCopy.ClientSecret = &redacted
		}
		sanitized.AAP = &aapCopy
	}

	return &sanitized
}
