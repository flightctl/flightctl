package e2e

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/flightctl/flightctl/internal/client"
	. "github.com/onsi/ginkgo/v2"
)

// ErrUnlessHTTPForbidden returns nil if err is a non-nil *APIError with HTTP 403 Forbidden.
// It's used in tests that expect an action to be denied. Otherwise returns a descriptive error.
func ErrUnlessHTTPForbidden(err error, desc string) error {
	if err == nil {
		return fmt.Errorf("%s: expected error (HTTP 403 Forbidden), got nil", desc)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return fmt.Errorf("%s: expected *APIError, got %T: %v", desc, err, err)
	}
	if !apiErr.IsStatusCode(http.StatusForbidden) {
		return fmt.Errorf("%s: expected HTTP 403 Forbidden, got %d", desc, apiErr.StatusCode)
	}
	return nil
}

func (h *Harness) GetClientAccessToken() (string, error) {
	if h == nil {
		return "", fmt.Errorf("harness is nil")
	}
	cfg, err := h.ReadClientConfig("")
	if err != nil {
		return "", fmt.Errorf("failed to read client config: %w", err)
	}
	if cfg == nil {
		return "", fmt.Errorf("client config is nil")
	}
	if cfg.AuthInfo.AccessToken == "" {
		return "", fmt.Errorf("access token is empty")
	}
	return cfg.AuthInfo.AccessToken, nil
}

// GetDirectClusterToken returns the current cluster token from the active oc context.
func (h *Harness) GetDirectClusterToken() (string, error) {
	if h == nil {
		return "", fmt.Errorf("harness is nil")
	}
	token, err := h.SH("oc", "whoami", "-t")
	if err != nil {
		return "", fmt.Errorf("failed to resolve direct cluster token from oc context: %w", err)
	}
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return "", fmt.Errorf("direct cluster token is empty")
	}
	return trimmed, nil
}

func (h *Harness) ResolveOrganizationAndClientToken() (string, string, error) {
	if h == nil {
		return "", "", fmt.Errorf("harness is nil")
	}

	orgID, err := h.GetOrganizationID()
	if err != nil {
		return "", "", fmt.Errorf("failed to resolve organization id: %w", err)
	}
	if orgID == "" {
		return "", "", fmt.Errorf("organization id is empty")
	}

	token, err := h.GetClientAccessToken()
	if err != nil {
		return "", "", fmt.Errorf("failed to resolve client access token: %w", err)
	}

	return orgID, token, nil
}

// SetCurrentOrganization sets the organization in the client config file and refreshes the harness client.
// Call after changing namespace/login so subsequent API calls use this org.
func (h *Harness) SetCurrentOrganization(organization string) error {
	if organization == "" {
		return nil
	}
	configPath, err := client.DefaultFlightctlClientConfigPath()
	if err != nil {
		return fmt.Errorf("failed to get client config path: %w", err)
	}
	cfg, err := client.ParseConfigFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}
	cfg.Organization = organization
	if err := cfg.Persist(configPath); err != nil {
		return fmt.Errorf("failed to persist config with organization %q: %w", organization, err)
	}
	GinkgoWriter.Printf("Set current organization to: %s\n", organization)
	return h.RefreshClient()
}

// GetCurrentUserPreferredUsername returns the preferred_username from the auth userinfo endpoint for the currently logged-in user.
func (h *Harness) GetCurrentUserPreferredUsername() (string, error) {
	if h == nil || h.Client == nil {
		return "", fmt.Errorf("harness or client is nil")
	}
	resp, err := h.Client.AuthUserInfoWithResponse(h.Context)
	if err != nil {
		return "", fmt.Errorf("auth userinfo request: %w", err)
	}
	if resp.JSON200 == nil {
		return "", fmt.Errorf("auth userinfo: no response body (status %d)", resp.StatusCode())
	}
	if resp.JSON200.PreferredUsername == nil || *resp.JSON200.PreferredUsername == "" {
		return "", fmt.Errorf("auth userinfo: preferred_username is empty")
	}
	return *resp.JSON200.PreferredUsername, nil
}

// GetOrganizationIDForNamespace returns the organization ID whose Spec.ExternalId matches the given namespace (e.g. OpenShift project).
// If none match, returns an error so callers can fall back to GetOrganizationID() if desired.
func (h *Harness) GetOrganizationIDForNamespace(namespace string) (string, error) {
	if namespace == "" {
		return "", fmt.Errorf("namespace is empty")
	}
	resp, err := h.Client.ListOrganizationsWithResponse(h.Context, nil)
	if err != nil {
		return "", err
	}
	if resp.JSON200 == nil {
		return "", fmt.Errorf("no organizations response")
	}
	for _, org := range resp.JSON200.Items {
		if org.Spec != nil && org.Spec.ExternalId != nil && *org.Spec.ExternalId == namespace {
			if org.Metadata.Name != nil && *org.Metadata.Name != "" {
				return *org.Metadata.Name, nil
			}
		}
	}
	return "", fmt.Errorf("no organization found with externalId (namespace) %q", namespace)
}
