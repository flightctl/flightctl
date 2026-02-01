// Package infra provides testcontainers-based infrastructure for E2E tests.
package infra

import (
	"context"
	"fmt"
)

// SecretsProvider abstracts secret access for different environments.
// K8s: reads from Kubernetes Secret API.
// Quadlet: not supported; GetSecretData returns an error so callers fall back to file/infra paths.
type SecretsProvider interface {
	// GetSecretData returns the value of a key in a secret.
	// Namespace and secretName identify the secret; key is the data key.
	// Returns decoded bytes (K8s API returns Secret.Data already decoded).
	GetSecretData(ctx context.Context, namespace, secretName, key string) ([]byte, error)

	// GetSecretDataForService returns the value of a key in a secret for the given service.
	// K8s: resolves service to namespace internally. Quadlet: returns ErrSecretsNotSupported.
	GetSecretDataForService(ctx context.Context, service ServiceName, secretName, key string) ([]byte, error)
}

// ErrSecretsNotSupported is returned when the environment does not support secret storage (e.g. quadlet).
var ErrSecretsNotSupported = fmt.Errorf("secrets not supported in this environment")
