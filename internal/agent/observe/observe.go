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
	name             string
	mu               sync.Mutex
	device           v1alpha1.Device
	hasSynced        bool
	managementClient *client.Management
	logPrefix        string
}

func NewDevice(name string) *Device {
	return &Device{
		name: name,
	}
}

type DeviceGetter interface {
	Get(ctx context.Context) (*v1alpha1.Device, error)
}

func (d *Device) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
			time.Sleep(10 * time.Second) //constant/ticker
			existingDevice := d.Get(ctx)
			newDevice, err := d.managementClient.GetDevice(ctx, d.name)
			if err != nil {
				klog.Errorf("%sfailed to get device: %v", d.logPrefix, err)
				continue
			}
			if equality.Semantic.DeepEqual(existingDevice, newDevice) {
				continue
			}
			d.set(*newDevice)
		}
	}
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

func (d *Device) HasSynced(ctx context.Context) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.hasSynced
}
