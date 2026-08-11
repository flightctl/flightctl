package authproviderhelpers

import (
	"fmt"
	"os"

	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
)

// BuildOIDCAuthProviderYAML renders a minimal OIDC AuthProvider manifest string.
// When enabled is false the provider is created in disabled state (safe for e2e tests
// that only need to exercise encryption or credential storage, not live authentication).
func BuildOIDCAuthProviderYAML(name, issuerURL, clientID, clientSecret string, enabled bool) string {
	return fmt.Sprintf(`apiVersion: flightctl.io/v1beta1
kind: AuthProvider
metadata:
  name: %q
spec:
  providerType: oidc
  displayName: %q
  issuer: %q
  clientId: %q
  clientSecret: %q
  enabled: %t
  scopes:
    - openid
    - profile
    - email
  usernameClaim:
    - preferred_username
  organizationAssignment:
    type: static
    organizationName: default
  roleAssignment:
    type: static
    roles:
      - flightctl-admin
`, name, name, issuerURL, clientID, clientSecret, enabled)
}

// ApplyManifest writes manifestYAML to a temp file and applies it via the harness CLI.
// Login must be established by the caller before invoking this function.
func ApplyManifest(harness *e2e.Harness, manifestYAML string) (string, error) {
	if harness == nil {
		return "", fmt.Errorf("ApplyManifest: harness is required")
	}
	if manifestYAML == "" {
		return "", fmt.Errorf("ApplyManifest: manifestYAML is empty")
	}
	tmp, err := os.CreateTemp("", "authprovider-manifest-*.yaml")
	if err != nil {
		GinkgoWriter.Printf("ApplyManifest: create temp file failed: %v\n", err)
		return "", fmt.Errorf("create temp manifest file: %w", err)
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close() //nolint:errcheck
	if _, err := tmp.WriteString(manifestYAML); err != nil {
		GinkgoWriter.Printf("ApplyManifest: write temp file failed: %v\n", err)
		return "", fmt.Errorf("write manifest: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close temp file: %w", err)
	}
	out, err := harness.ApplyResource(tmp.Name())
	if err != nil {
		GinkgoWriter.Printf("ApplyManifest: ApplyResource failed: %v\n", err)
	}
	return out, err
}

// DeleteAuthProvider deletes an AuthProvider CR by name via the harness CLI.
// Login must be established by the caller before invoking this function.
// Returns the CLI output (e.g. "authprovider/name deleted") so callers can assert on it.
func DeleteAuthProvider(harness *e2e.Harness, name string) (string, error) {
	if harness == nil {
		return "", fmt.Errorf("DeleteAuthProvider: harness is required")
	}
	if name == "" {
		return "", fmt.Errorf("DeleteAuthProvider: name is required")
	}
	out, err := harness.ManageResource("delete", "authprovider", name)
	if err != nil {
		GinkgoWriter.Printf("DeleteAuthProvider: ManageResource failed for %q: %v\n", name, err)
	}
	return out, err
}
