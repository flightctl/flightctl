package certmanager

import (
	"fmt"
	"path/filepath"

	"github.com/flightctl/flightctl/internal/agent/client"
	agent_config "github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device/certmanager/provider/config"
	"github.com/flightctl/flightctl/internal/agent/device/certmanager/provider/provisioner"
	"github.com/flightctl/flightctl/internal/agent/device/certmanager/provider/storage"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
)

// WithBuiltins registers the standard certificate manager providers and factories.
func WithBuiltins(
	deviceName string,
	managementClient client.Management,
	readWriter fileio.ReadWriter,
	cfg *agent_config.Config,
) ManagerOption {
	return func(cm *CertManager) error {
		if cfg == nil {
			return fmt.Errorf("nil agent config")
		}
		if managementClient == nil {
			return fmt.Errorf("nil management client")
		}
		if readWriter == nil {
			return fmt.Errorf("nil read-writer")
		}

		// Config providers
		if err := WithConfigProvider(config.NewDropInConfigProvider(readWriter, filepath.Join(cfg.ConfigDir, "certs.yaml")))(cm); err != nil {
			return err
		}

		// Provisioner providers
		if err := WithProvisionerProvider(provisioner.NewCSRProvisionerFactory(deviceName, managementClient))(cm); err != nil {
			return err
		}

		// Storage providers
		if err := WithStorageProvider(storage.NewFileSystemStorageFactory(readWriter))(cm); err != nil {
			return err
		}

		return nil
	}
}
