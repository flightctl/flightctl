package e2e

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/client"
	. "github.com/onsi/ginkgo/v2"
)

// GetClientAccessToken returns the access token from the current client config.
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

func (h *Harness) ResolveClusterLoginContext(ctx context.Context) (string, string, error) {
	if h == nil {
		return "", "", fmt.Errorf("harness is nil")
	}
	if ctx == nil {
		return "", "", fmt.Errorf("context is nil")
	}

	defaultK8sContext, err := h.GetDefaultK8sAdminContext()
	if err != nil {
		return "", "", fmt.Errorf("failed to get default k8s context: %w", err)
	}
	k8sAPIEndpoint, err := h.GetK8sApiEndpoint(ctx, defaultK8sContext)
	if err != nil {
		return "", "", fmt.Errorf("failed to get k8s api endpoint for context %q: %w", defaultK8sContext, err)
	}

	return defaultK8sContext, k8sAPIEndpoint, nil
}

func (h *Harness) RestoreK8sContext(ctx context.Context, k8sContext string) error {
	if h == nil {
		return fmt.Errorf("harness is nil")
	}
	if ctx == nil {
		return fmt.Errorf("context is nil")
	}
	if k8sContext == "" {
		return fmt.Errorf("k8s context is empty")
	}
	_, err := h.ChangeK8sContext(ctx, k8sContext)
	if err != nil {
		return fmt.Errorf("failed to restore k8s context %q: %w", k8sContext, err)
	}
	return nil
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
