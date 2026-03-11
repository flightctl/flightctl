package infra

import (
	"context"
	"fmt"
)

// TPMProvider abstracts TPM certificate management for different environments.
// K8s: creates ConfigMap, updates API config, adds volume mount, restarts deployment.
// Quadlet: writes certs to config directory, updates config file, restarts service.
type TPMProvider interface {
	// InjectCerts configures TPM CA certificates for the API server.
	// certs is a map of filename -> PEM certificate data.
	// The implementation handles all environment-specific details:
	// - K8s: creates ConfigMap, updates API config with tpmCAPaths, adds volume mount, restarts deployment
	// - Quadlet: writes certs to config directory, updates config file, restarts service
	InjectCerts(ctx context.Context, certs map[string][]byte) error

	// CleanupCerts removes TPM CA certificates from the API server configuration.
	// - K8s: deletes ConfigMap, removes volume mount from deployment, restarts deployment
	// - Quadlet: removes cert files, updates config to remove tpmCAPaths, restarts service
	CleanupCerts(ctx context.Context) error
}

// ErrTPMNotSupported is returned when the environment does not support TPM operations.
var ErrTPMNotSupported = fmt.Errorf("TPM operations not supported in this environment")
