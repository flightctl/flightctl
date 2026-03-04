// Package quadlet provides Quadlet/systemd-specific implementations of the infra providers.
package quadlet

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/flightctl/flightctl/test/e2e/infra"
)

// SecretsProvider implements infra.SecretsProvider for Quadlet environments.
// It reads secret values via `podman secret inspect --showsecret <name>`.
type SecretsProvider struct {
	infra *InfraProvider
}

// NewSecretsProvider creates a new Quadlet SecretsProvider. Pass the Quadlet InfraProvider so it can run podman commands (with sudo/SSH if configured).
func NewSecretsProvider(infraP *InfraProvider) infra.SecretsProvider {
	return &SecretsProvider{infra: infraP}
}

// GetSecretData returns ErrSecretsNotSupported. Callers should fall back to infra or local file paths.
func (p *SecretsProvider) GetSecretData(_ context.Context, _, _, _ string) ([]byte, error) {
	return nil, infra.ErrSecretsNotSupported
}

// CreateSecret is not supported for Quadlet; secrets are file-based.
func (p *SecretsProvider) CreateSecret(ctx context.Context, namespace, name string, stringData map[string]string) error {
	return infra.ErrSecretsNotSupported
}

// podmanSecretInspectOutput matches the JSON from `podman secret inspect --showsecret` (array of one element).
type podmanSecretInspectOutput []struct {
	SecretData string `json:"SecretData"`
}

// resolvePodmanSecretName maps caller secretName (K8s-style e.g. "flightctl-kv-secret") to Podman secret name (e.g. "flightctl-kv-password").
func resolvePodmanSecretName(callerSecretName string) string {
	return strings.Replace(callerSecretName, "-secret", "-password", 1)
}

// GetSecretDataForService reads the secret from Podman with `podman secret inspect --showsecret <name>` and returns SecretData.
// Caller secretName (e.g. "flightctl-kv-secret") is mapped to Podman secret name (e.g. "flightctl-kv-password") by convention.
func (p *SecretsProvider) GetSecretDataForService(_ context.Context, _ infra.ServiceName, secretName, key string) ([]byte, error) {
	if p.infra == nil {
		return nil, infra.ErrSecretsNotSupported
	}
	podmanName := resolvePodmanSecretName(secretName)
	if podmanName == "" {
		return nil, infra.ErrSecretsNotSupported
	}
	out, err := p.infra.RunCommand("podman", "secret", "inspect", "--showsecret", podmanName)
	if err != nil {
		return nil, infra.ErrSecretsNotSupported
	}
	var parsed podmanSecretInspectOutput
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return nil, infra.ErrSecretsNotSupported
	}
	if len(parsed) == 0 {
		return nil, infra.ErrSecretsNotSupported
	}
	data := strings.TrimSpace(parsed[0].SecretData)
	if data == "" {
		return nil, infra.ErrSecretsNotSupported
	}
	return []byte(data), nil
}
