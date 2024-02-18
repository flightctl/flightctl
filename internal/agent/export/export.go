package export

import (
	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/export/devicestatus"
)

var _ DeviceStatus = (*DeviceStatusExporter)(nil)

type DeviceStatus interface {
	// Status returns the device status collected from all device status exporters.
	devicestatus.Getter
}

type DeviceStatusExporter struct {
	status devicestatus.Getter
}

func NewDeviceStatus(
	status devicestatus.Getter,
) DeviceStatus {
	return &DeviceStatusExporter{
		status: status,
	}
}

func (e *DeviceStatusExporter) Get() v1alpha1.DeviceStatus {
	return e.status.Get()
}
