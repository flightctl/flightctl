package provider

import (
	"fmt"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
)

// NormalizeOIDCProviderSpecURLs canonicalizes issuer-identity URL fields on an OIDC spec
// by stripping a trailing path slash. Host case and other RFC 3986 variations are left unchanged.
func NormalizeOIDCProviderSpecURLs(spec *api.OIDCProviderSpec) error {
	if spec == nil {
		return nil
	}
	if spec.Issuer == "" {
		return nil
	}
	normalized, err := NormalizeIssuerURL(spec.Issuer)
	if err != nil {
		return fmt.Errorf("invalid OIDC issuer URL: %w", err)
	}
	spec.Issuer = normalized
	return nil
}

// NormalizeOAuth2ProviderSpecURLs canonicalizes OAuth2 URL fields used for identity and
// uniqueness (authorizationUrl, issuer, tokenUrl, userinfoUrl) by stripping a trailing path slash.
func NormalizeOAuth2ProviderSpecURLs(spec *api.OAuth2ProviderSpec) error {
	if spec == nil {
		return nil
	}
	if spec.AuthorizationUrl != "" {
		normalized, err := NormalizeIssuerURL(spec.AuthorizationUrl)
		if err != nil {
			return fmt.Errorf("invalid OAuth2 authorizationUrl: %w", err)
		}
		spec.AuthorizationUrl = normalized
	}
	if spec.Issuer != nil && *spec.Issuer != "" {
		normalized, err := NormalizeIssuerURL(*spec.Issuer)
		if err != nil {
			return fmt.Errorf("invalid OAuth2 issuer URL: %w", err)
		}
		spec.Issuer = &normalized
	}
	if spec.TokenUrl != "" {
		normalized, err := NormalizeIssuerURL(spec.TokenUrl)
		if err != nil {
			return fmt.Errorf("invalid OAuth2 tokenUrl: %w", err)
		}
		spec.TokenUrl = normalized
	}
	if spec.UserinfoUrl != "" {
		normalized, err := NormalizeIssuerURL(spec.UserinfoUrl)
		if err != nil {
			return fmt.Errorf("invalid OAuth2 userinfoUrl: %w", err)
		}
		spec.UserinfoUrl = normalized
	}
	return nil
}

// NormalizeAuthProviderSpecURLs normalizes URL fields in auth provider specs before storing
// or constructing auth middleware. Unknown/malformed provider types are skipped so callers
// can surface validation errors separately.
func NormalizeAuthProviderSpecURLs(spec *api.AuthProviderSpec) error {
	if spec == nil {
		return nil
	}
	discriminator, err := spec.Discriminator()
	if err != nil {
		return nil
	}

	switch discriminator {
	case string(api.Oidc):
		oidcSpec, err := spec.AsOIDCProviderSpec()
		if err != nil {
			return fmt.Errorf("invalid OIDC provider spec: %w", err)
		}
		if err := NormalizeOIDCProviderSpecURLs(&oidcSpec); err != nil {
			return err
		}
		if mergeErr := spec.MergeOIDCProviderSpec(oidcSpec); mergeErr != nil {
			return fmt.Errorf("failed to update OIDC provider spec: %w", mergeErr)
		}

	case string(api.Oauth2):
		oauth2Spec, err := spec.AsOAuth2ProviderSpec()
		if err != nil {
			return fmt.Errorf("invalid OAuth2 provider spec: %w", err)
		}
		if err := NormalizeOAuth2ProviderSpecURLs(&oauth2Spec); err != nil {
			return err
		}
		if mergeErr := spec.MergeOAuth2ProviderSpec(oauth2Spec); mergeErr != nil {
			return fmt.Errorf("failed to update OAuth2 provider spec: %w", mergeErr)
		}
	}

	return nil
}
