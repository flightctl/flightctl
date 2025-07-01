package cert

import (
	"context"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	agent_config "github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device/cert/provider"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/pkg/log"
)

var _ Manager = (*CertManager)(nil)

type CertManager struct {
	deviceName       string
	managementClient client.Management
	deviceReadWriter fileio.ReadWriter
	specManager      spec.Manager
	statusManager    status.Manager
	log              *log.PrefixLogger
	cfg              *agent_config.Config
}

func NewManager(
	deviceName string,
	cfg *agent_config.Config,
	deviceReadWriter fileio.ReadWriter,
	log *log.PrefixLogger,
) *CertManager {

	return &CertManager{
		deviceName:       deviceName,
		deviceReadWriter: deviceReadWriter,
		specManager:      nil,
		statusManager:    nil,
		log:              log,
		cfg:              cfg,
	}
}

func (m *CertManager) Sync(ctx context.Context, current, desired *v1alpha1.DeviceSpec) error {
	// this controller currently does not implement a sync operation
	if desired.Certs == nil {
		return nil
	}
	certs := *desired.Certs
	a, _ := certs[0].Storage.AsFileSystemCertStorageProvider()
	p := provider.NewFileSystemStorage(a.FileSystem.CertPath, a.FileSystem.KeyPath, m.deviceReadWriter, m.log)
	w, _ := p.Writer(ctx)
	w.Write([]byte{'a'}, []byte{'b'})

	pr, _ := provider.NewCSRProvisioner(m.deviceName, m.managementClient, certs[0].Name, "flightctl.io/device-svc-client")
	err := pr.Provision(ctx)
	if err != nil {
		return err
	}
	pr.Result(ctx, w)

	return nil
}

func (m *CertManager) SetClient(managementClient client.Management) {
	//m.mu.Lock()
	//defer m.mu.Unlock()
	m.managementClient = managementClient
}
