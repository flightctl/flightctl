package identity

import (
	"fmt"

	"github.com/flightctl/flightctl/internal/tpm"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	// IdentityTypeSoftware represents file-based (software) identity
	IdentityTypeSoftware = "software"
	// IdentityTypeTPM represents TPM-based identity
	IdentityTypeTPM = "tpm"
)

// ExportableFactory creates ExportableProvider instances for different identity types.
// The factory is initialized with the capabilities available to the agent and
// returns errors when unsupported identity types are requested.
type ExportableFactory interface {
	// NewExportableProvider creates an ExportableProvider for the specified identity type.
	// Returns an error if the requested identity type is not supported by this factory.
	NewExportableProvider(identityType string) (ExportableProvider, error)
	// CanProvide returns true if the factory is able to provide the requested type
	CanProvide(identityType string) bool
}

// exportableFactory implements ExportableFactory with support for both software and TPM identities
// depending on the capabilities provided during initialization.
type exportableFactory struct {
	tpmClient tpm.Client
	log       *log.PrefixLogger
}

// NewExportableFactory creates a new ExportableFactory with the specified capabilities.
// If tpmClient is nil, the factory will only support software-based identities.
// If tpmClient is provided, the factory supports both software and TPM identities.
func NewExportableFactory(
	tpmClient tpm.Client,
	log *log.PrefixLogger,
) ExportableFactory {
	return &exportableFactory{
		tpmClient: tpmClient,
		log:       log,
	}
}

// NewExportableProvider creates an ExportableProvider for the specified identity type.
// Supported types: "software", "tpm", or ""
// Returns an error if the requested type is not supported by this factory's configuration.
func (f *exportableFactory) NewExportableProvider(identityType string) (ExportableProvider, error) {
	switch identityType {
	case "":
		// when given a choice, default TPM if available
		if f.tpmClient != nil {
			return f.createTPMProvider()
		}
		return f.createSoftwareProvider()
	case IdentityTypeSoftware:
		return f.createSoftwareProvider()
	case IdentityTypeTPM:
		return f.createTPMProvider()
	default:
		return nil, fmt.Errorf("unsupported identity type: %s (supported: %s, %s)",
			identityType, IdentityTypeSoftware, IdentityTypeTPM)
	}
}

// CanProvide indicates whether the factory can provide the specified type
func (f *exportableFactory) CanProvide(identityType string) bool {
	switch identityType {
	case "":
		return true
	case IdentityTypeSoftware:
		return true
	case IdentityTypeTPM:
		return f.tpmClient != nil
	default:
		return false
	}
}

// createSoftwareProvider creates a file-based identity provider.
// This is always available regardless of TPM configuration.
func (f *exportableFactory) createSoftwareProvider() (ExportableProvider, error) {
	return newSoftwareExportableProvider(), nil
}

// createTPMProvider creates a TPM-based identity provider.
// Returns an error if no TPM client is available.
func (f *exportableFactory) createTPMProvider() (ExportableProvider, error) {
	if f.tpmClient == nil {
		return nil, fmt.Errorf("tpm provider not initialized")
	}
	return newTPMExportableProvider(f.tpmClient), nil
}
