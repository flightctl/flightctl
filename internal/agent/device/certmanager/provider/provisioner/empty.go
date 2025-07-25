package provisioner

import (
	"context"
	"crypto/x509"
	"fmt"

	"github.com/flightctl/flightctl/internal/agent/device/certmanager/provider"
)

// EmptyProvisioner is a placeholder provisioner that always fails to provision certificates.
// It's used for testing, development, or as a fallback when no actual provisioning is needed.
// This provisioner always returns an error when asked to provision a certificate.
type EmptyProvisioner struct {
}

// NewEmptyProvisioner creates a new empty provisioner instance.
// This provisioner is stateless and requires no configuration.
func NewEmptyProvisioner() (*EmptyProvisioner, error) {
	return &EmptyProvisioner{}, nil
}

// Provision always fails with an error indicating this is an empty provisioner.
// This method never returns a certificate and is intended for testing or placeholder scenarios.
func (p *EmptyProvisioner) Provision(ctx context.Context) (bool, *x509.Certificate, []byte, error) {
	return false, nil, nil, fmt.Errorf("empty provisioner")
}

// EmptyProvisionerFactory implements ProvisionerFactory for empty provisioners.
// It creates empty provisioners that always fail, useful for testing and development.
type EmptyProvisionerFactory struct {
}

// NewEmptyProvisionerFactory creates a new empty provisioner factory.
// This factory is stateless and requires no configuration.
func NewEmptyProvisionerFactory() *EmptyProvisionerFactory {
	return &EmptyProvisionerFactory{}
}

// Type returns the provisioner type string used as map key in the certificate manager.
func (f *EmptyProvisionerFactory) Type() string {
	return "empty"
}

// New creates a new EmptyProvisioner instance.
// The certificate configuration is ignored since empty provisioners have no configuration.
func (f *EmptyProvisionerFactory) New(log provider.Logger, cc provider.CertificateConfig) (provider.ProvisionerProvider, error) {
	return NewEmptyProvisioner()
}

// Validate always returns nil since empty provisioners have no configuration to validate.
// This allows empty provisioners to be used in any certificate configuration.
func (f *EmptyProvisionerFactory) Validate(log provider.Logger, cc provider.CertificateConfig) error {
	return nil
}
