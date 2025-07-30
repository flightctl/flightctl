package identity

import (
	"context"
	"crypto"
	"fmt"

	grpc_v1 "github.com/flightctl/flightctl/api/grpc/v1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	base_client "github.com/flightctl/flightctl/internal/client"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/flightctl/flightctl/pkg/log"
)

var _ Provider = (*fileProvider)(nil)

type fileProvider struct {
	deviceName     string
	clientKeyPath  string
	clientCertPath string
	privateKey     crypto.PrivateKey
	rw             fileio.ReadWriter
	log            *log.PrefixLogger
}

// newFileProvider creates a new file-based identity provider
func newFileProvider(
	clientKeyPath string,
	clientCertPath string,
	rw fileio.ReadWriter,
	log *log.PrefixLogger,
) *fileProvider {
	return &fileProvider{
		clientKeyPath:  clientKeyPath,
		clientCertPath: clientCertPath,
		rw:             rw,
		log:            log,
	}
}

func (f *fileProvider) Initialize(ctx context.Context) error {
	publicKey, privateKey, _, err := fccrypto.EnsureKey(f.rw.PathFor(f.clientKeyPath))
	if err != nil {
		return fmt.Errorf("failed to ensure key: %w", err)
	}
	f.privateKey = privateKey

	// generate device name from public key hash
	f.deviceName, err = generateDeviceName(publicKey)
	if err != nil {
		return err
	}

	return nil
}

func (f *fileProvider) GetDeviceName() (string, error) {
	return f.deviceName, nil
}

func (f *fileProvider) GenerateCSR(deviceName string) ([]byte, error) {
	if f.privateKey == nil {
		return nil, ErrNotInitialized
	}
	signer, ok := f.privateKey.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("private key does not implement crypto.Signer")
	}
	return fccrypto.MakeCSR(signer, deviceName)
}

func (f *fileProvider) StoreCertificate(certPEM []byte) error {
	return f.rw.WriteFile(f.clientCertPath, certPEM, 0600)
}

func (f *fileProvider) HasCertificate() bool {
	exists, err := f.rw.PathExists(f.clientCertPath)
	if err != nil {
		f.log.Warnf("Failed to check certificate existence: %v", err)
		return false
	}
	return exists
}

func (f *fileProvider) CreateManagementClient(config *base_client.Config, metricsCallback client.RPCMetricsCallback) (client.Management, error) {
	// check if management certificate exists
	managementCertExists, err := f.rw.PathExists(config.GetClientCertificatePath())
	if err != nil {
		return nil, fmt.Errorf("checking certificate file %q: %w", config.GetClientCertificatePath(), err)
	}

	if !managementCertExists {
		return nil, fmt.Errorf("management client certificate does not exist at %q - device needs re-enrollment", config.GetClientCertificatePath())
	}

	httpClient, err := client.NewFromConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create management client: %w", err)
	}

	managementClient := client.NewManagement(httpClient, metricsCallback)
	return managementClient, nil
}

func (f *fileProvider) CreateGRPCClient(config *base_client.Config) (grpc_v1.RouterServiceClient, error) {
	// check if management certificate exists
	managementCertExists, err := f.rw.PathExists(config.GetClientCertificatePath())
	if err != nil {
		return nil, fmt.Errorf("checking certificate file %q: %w", config.GetClientCertificatePath(), err)
	}

	if !managementCertExists {
		return nil, fmt.Errorf("management client certificate does not exist at %q - device needs re-enrollment", config.GetClientCertificatePath())
	}

	return base_client.NewGRPCClientFromConfig(config, "")
}

func (f *fileProvider) WipeCredentials() error {
	var errs []error

	f.privateKey = nil

	if f.clientCertPath != "" {
		f.log.Infof("Wiping certificate file %s", f.clientCertPath)
		if err := f.rw.OverwriteAndWipe(f.clientCertPath); err != nil {
			errs = append(errs, fmt.Errorf("failed to wipe certificate file %s: %w", f.clientCertPath, err))
		}
	}

	if f.clientKeyPath != "" {
		f.log.Infof("Wiping key file %s", f.clientKeyPath)
		if err := f.rw.OverwriteAndWipe(f.clientKeyPath); err != nil {
			errs = append(errs, fmt.Errorf("failed to wipe key file %s: %w", f.clientKeyPath, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("failed to wipe credentials: %v", errs)
	}

	f.log.Info("Successfully wiped file-based credentials")
	return nil
}

func (f *fileProvider) Close(_ context.Context) error {
	// no-op for file provider
	return nil
}
