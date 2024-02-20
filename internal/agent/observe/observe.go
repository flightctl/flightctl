package observe

import (
	"context"
	"sync"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/client"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/klog/v2"
)

type Observer interface {
	Run(context.Context) error
	HasSynced(context.Context) bool
}

var _ Observer = (*Device)(nil)

type Device struct {
	name                   string
	mu                     sync.Mutex
	device                 v1alpha1.Device
	hasSynced              bool
	managementClient       *client.Management
	managementEndpoint     string
	managementCertFilePath string
	agentKeyFilePath       string
	caCertFilePath         string
	logPrefix              string
	fetchInterval          time.Duration
}

func NewDevice(
	name string,
	fetchInterval time.Duration,
	caCertFilePath string,
	managementCertFilePath string,
	agentKeyFilePath string,
	managementEndpoint string,
) *Device {
	return &Device{
		name:                   name,
		fetchInterval:          fetchInterval,
		caCertFilePath:         caCertFilePath,
		managementCertFilePath: managementCertFilePath,
		agentKeyFilePath:       agentKeyFilePath,
		managementEndpoint:     managementEndpoint,
		device: v1alpha1.Device{
			ApiVersion: "v1alpha1",
			Kind:       "Device",
			Status:     &v1alpha1.DeviceStatus{},
			Metadata: v1alpha1.ObjectMeta{
				Name: &name,
			},
		},
	}
}

type DeviceGetter interface {
	Get(ctx context.Context) (*v1alpha1.Device, error)
}

func (d *Device) Run(ctx context.Context) error {
	ticker := time.NewTicker(d.fetchInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := d.ensureClient(); err != nil {
				klog.V(4).Infof("%smanagement client is not ready", d.logPrefix)
				continue
			}
			existingDevice := d.Get(ctx)
			newDevice, err := d.managementClient.GetDevice(ctx, d.name)
			if err != nil {
				klog.Errorf("%sfailed to get device: %v", d.logPrefix, err)
				continue
			}
			if equality.Semantic.DeepEqual(existingDevice.Spec, newDevice.Spec) {
				continue
			}
			d.set(*newDevice)
		}
	}
}

func (d *Device) ensureClient() error {
	if d.managementClient != nil {
		return nil
	}
	managementHTTPClient, err := client.NewWithResponses(d.managementEndpoint, d.caCertFilePath, d.managementCertFilePath, d.agentKeyFilePath)
	if err != nil {
		return err
	}
	d.managementClient = client.NewManagement(managementHTTPClient)
	return nil
}

func (d *Device) Get(ctx context.Context) *v1alpha1.Device {
	d.mu.Lock()
	defer d.mu.Unlock()
	return &d.device
}

func (d *Device) set(device v1alpha1.Device) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.device = device
	d.hasSynced = true
}

func (d *Device) HasSynced(context.Context) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.hasSynced
}
