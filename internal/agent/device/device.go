package device

import (
	"context"
	"sync"

	"github.com/flightctl/flightctl/api/v1alpha1"
)

// New returns a new device.
func New(name string) *Device {
	return &Device{
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

type Device struct {
	// mutex to protect the device resource
	mu sync.RWMutex
	// The device resource manifest
	device v1alpha1.Device
}

func (d *Device) Name() string {
	return *d.device.Metadata.Name
}

// Set updates the local device resource.
func (d *Device) Set(r v1alpha1.Device) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.device = r
}

// Get returns a reference to the device resource.
func (d *Device) Get(context.Context) *v1alpha1.Device {
	defer d.mu.RUnlock()
	return &d.device
}
