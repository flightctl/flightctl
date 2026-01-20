package identity

import (
	"context"
	"crypto"
	"fmt"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	grpc_v1 "github.com/flightctl/flightctl/api/grpc/v1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	baseclient "github.com/flightctl/flightctl/internal/client"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/flightctl/flightctl/pkg/log"
	"k8s.io/client-go/util/cert"
)

var _ Provider = (*fileProvider)(nil)

type fileProvider struct {
	deviceName     string
	clientKeyPath  string
	clientCertPath string
	clientCSRPath  string
	privateKey     crypto.PrivateKey
	rw             fileio.ReadWriter
	log            *log.PrefixLogger
}

// newFileProvider creates a new file-based identity provider
func newFileProvider(
	clientKeyPath string,
	clientCertPath string,
	clientCSRPath string,
	rw fileio.ReadWriter,
	log *log.PrefixLogger,
) *fileProvider {
	return &fileProvider{
		clientKeyPath:  clientKeyPath,
		clientCertPath: clientCertPath,
		clientCSRPath:  clientCSRPath,
		rw:             rw,
		log:            log,
	}
}

type softwareExportableProvider struct {
}

func newSoftwareExportableProvider() *softwareExportableProvider {
	return &softwareExportableProvider{}
}
func (f *softwareExportableProvider) NewExportable(name string) (*Exportable, error) {
	_, priv, err := fccrypto.NewKeyPair()
	if err != nil {
		return nil, fmt.Errorf("creating key pair: %q: %w", name, err)
	}
	signer, ok := priv.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("expected crypto.Signer, got %T", priv)
	}

	csr, err := fccrypto.MakeCSR(signer, name)
	if err != nil {
		return nil, fmt.Errorf("creating CSR: %w", err)
	}

	pem, err := fccrypto.PEMEncodeKey(priv)
	if err != nil {
		return nil, fmt.Errorf("encoding private key: %w", err)
	}

	return &Exportable{
		name:   name,
		csr:    csr,
		keyPEM: pem,
	}, nil
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

func (f *fileProvider) ProveIdentity(ctx context.Context, enrollmentRequest *v1beta1.EnrollmentRequest) error {
	// no-op for file provider since identity is proven by CSR signing with private key
	return nil
}

func (f *fileProvider) StoreCertificate(certPEM []byte) error {
	return f.rw.WriteFile(f.clientCertPath, certPEM, 0600)
}

func (f *fileProvider) HasCertificate() bool {
	return hasCertificate(f.rw, f.clientCertPath, f.log)
}

func (f *fileProvider) GetCertificate() ([]byte, error) {
	if f.clientCertPath == "" {
		return nil, nil
	}

	exists, err := f.rw.PathExists(f.clientCertPath)
	if err != nil {
		return nil, fmt.Errorf("checking certificate file %q: %w", f.clientCertPath, err)
	}
	if !exists {
		return nil, nil
	}

	pemBytes, err := f.rw.ReadFile(f.clientCertPath)
	if err != nil {
		return nil, fmt.Errorf("reading certificate file %q: %w", f.clientCertPath, err)
	}

	if _, err := cert.ParseCertsPEM(pemBytes); err != nil {
		return nil, fmt.Errorf("parsing certificate file %q: %w", f.clientCertPath, err)
	}

	return pemBytes, nil
}

func (f *fileProvider) CreateManagementClient(config *baseclient.Config, metricsCallback client.RPCMetricsCallback) (client.Management, error) {
	// check if management certificate exists
	managementCertExists, err := f.rw.PathExists(config.GetClientCertificatePath())
	if err != nil {
		return nil, fmt.Errorf("checking certificate file %q: %w", config.GetClientCertificatePath(), err)
	}

	if !managementCertExists {
		return nil, fmt.Errorf("management client certificate does not exist at %q - device needs re-enrollment", config.GetClientCertificatePath())
	}

	return client.NewManagementDelegate(func() (client.Management, error) {
		httpClient, err := client.NewFromConfig(config, f.log)
		if err != nil {
			return nil, fmt.Errorf("create management client: %w", err)
		}
		return client.NewManagement(httpClient, metricsCallback), nil
	})
}

func (f *fileProvider) CreateGRPCClient(config *baseclient.Config) (grpc_v1.RouterServiceClient, error) {
	// check if management certificate exists
	managementCertExists, err := f.rw.PathExists(config.GetClientCertificatePath())
	if err != nil {
		return nil, fmt.Errorf("checking certificate file %q: %w", config.GetClientCertificatePath(), err)
	}

	if !managementCertExists {
		return nil, fmt.Errorf("management client certificate does not exist at %q - device needs re-enrollment", config.GetClientCertificatePath())
	}

	return baseclient.NewGRPCClientFromConfig(config, "")
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

	if f.clientCSRPath != "" {
		f.log.Infof("Wiping CSR file %s", f.clientCSRPath)
		if err := f.rw.OverwriteAndWipe(f.clientCSRPath); err != nil {
			errs = append(errs, fmt.Errorf("failed to wipe CSR file %s: %w", f.clientCSRPath, err))
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

func (f *fileProvider) WipeCertificateOnly() error {
	var errs []error

	// Only wipe the certificate file, not the key or CSR
	if f.clientCertPath != "" {
		f.log.Infof("Wiping certificate file %s", f.clientCertPath)
		if err := f.rw.OverwriteAndWipe(f.clientCertPath); err != nil {
			errs = append(errs, fmt.Errorf("failed to wipe certificate file %s: %w", f.clientCertPath, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("failed to wipe certificate: %v", errs)
	}

	f.log.Info("Successfully wiped certificate file")
	return nil
}

func (f *fileProvider) Close(_ context.Context) error {
	// no-op for file provider
	return nil
}
