package provider

import (
	"fmt"
	"net/url"
	"strings"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
)

// InferOAuth2IntrospectionConfig attempts to infer a sensible introspection configuration
// based on the OAuth2 provider URLs. Returns an error if no introspection can be inferred.
func InferOAuth2IntrospectionConfig(spec api.OAuth2ProviderSpec) (*api.OAuth2Introspection, error) {
	// Check if this is a GitHub OAuth2 provider
	if strings.Contains(strings.ToLower(spec.AuthorizationUrl), "github") ||
		strings.Contains(strings.ToLower(spec.TokenUrl), "github") {
		introspection := &api.OAuth2Introspection{}
		githubSpec := api.GitHubIntrospectionSpec{
			Type: api.Github,
		}
		// Set URL if it's GitHub Enterprise (not github.com)
		if !strings.Contains(strings.ToLower(spec.AuthorizationUrl), "github.com") {
			// Extract base URL from authorization URL for GitHub Enterprise
			// e.g., https://github.enterprise.com/login/oauth/authorize -> https://api.github.enterprise.com
			baseURL := extractGitHubEnterpriseBaseURL(spec.AuthorizationUrl)
			if baseURL != "" {
				githubSpec.Url = &baseURL
			}
		}
		_ = introspection.FromGitHubIntrospectionSpec(githubSpec)
		return introspection, nil
	}

	// Try to infer RFC 7662 introspection endpoint
	// Common patterns: {tokenUrl}/introspect, {issuer}/introspect
	introspectionURL := inferRFC7662IntrospectionURL(spec)
	if introspectionURL != "" {
		introspection := &api.OAuth2Introspection{}
		rfc7662Spec := api.Rfc7662IntrospectionSpec{
			Type: api.Rfc7662,
			Url:  introspectionURL,
		}
		_ = introspection.FromRfc7662IntrospectionSpec(rfc7662Spec)
		return introspection, nil
	}

	// No introspection could be inferred - reject
	return nil, fmt.Errorf("could not infer introspection configuration from provided URLs (authorizationUrl: %s, tokenUrl: %s); please specify introspection field explicitly", spec.AuthorizationUrl, spec.TokenUrl)
}

// extractGitHubEnterpriseBaseURL extracts the API base URL for GitHub Enterprise
// from an authorization URL like https://github.enterprise.com/login/oauth/authorize
func extractGitHubEnterpriseBaseURL(authURL string) string {
	// Try to parse the URL to extract the host
	if idx := strings.Index(authURL, "://"); idx != -1 {
		rest := authURL[idx+3:]
		if endIdx := strings.Index(rest, "/"); endIdx != -1 {
			host := rest[:endIdx]
			// For GitHub Enterprise, the API is typically at {host}/api/v3
			scheme := authURL[:idx]
			return scheme + "://" + host + "/api/v3"
		}
	}
	return ""
}

// inferRFC7662IntrospectionURL attempts to infer the RFC 7662 introspection endpoint URL
// based on common OAuth2 provider patterns
func inferRFC7662IntrospectionURL(spec api.OAuth2ProviderSpec) string {
	// Pattern 1: {tokenUrl}/introspect (most common)
	if spec.TokenUrl != "" {
		if introspectURL := buildIntrospectionURL(spec.TokenUrl); introspectURL != "" {
			return introspectURL
		}
	}

	// Pattern 2: {issuer}/introspect
	if spec.Issuer != nil && *spec.Issuer != "" {
		if introspectURL := buildIntrospectionURL(*spec.Issuer); introspectURL != "" {
			return introspectURL
		}
	}

	return ""
}

// buildIntrospectionURL constructs an introspection URL from a base URL,
// properly handling query parameters and URL components
func buildIntrospectionURL(baseURL string) string {
	parsedURL, err := url.Parse(baseURL)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		return ""
	}

	// Remove trailing slash from path
	path := strings.TrimSuffix(parsedURL.Path, "/")

	// Check if path ends with /token and replace with /introspect
	if strings.HasSuffix(path, "/token") {
		path = strings.TrimSuffix(path, "/token") + "/introspect"
	} else {
		// Otherwise append /introspect
		path = path + "/introspect"
	}

	// Update the path in the parsed URL
	parsedURL.Path = path

	// Return the reassembled URL with all components preserved
	return parsedURL.String()
}
