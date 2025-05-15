package agent

import (
	"github.com/flightctl/flightctl/internal/cloudevents/wrapper"

	"open-cluster-management.io/sdk-go/pkg/cloudevents/generic/types"
)

type deviceLister struct {
	devices *deviceCache
}

func NewDeviceLister(devices *deviceCache) *deviceLister {
	return &deviceLister{devices: devices}
}

func (l *deviceLister) List(options types.ListOptions) ([]*wrapper.Device, error) {
	devices := []*wrapper.Device{}
	for _, d := range l.devices.List() {
		devices = append(devices, &wrapper.Device{Device: d})
	}
	return devices, nil
}
