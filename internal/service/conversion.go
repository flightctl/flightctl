package service

import (
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/model"
	"github.com/flightctl/flightctl/pkg/util"
)

var (
	DeviceAPI      = "v1alpha1"
	DeviceKind     = "device"
	DeviceListKind = "deviceList"
)

func DeviceModelToApi(device model.Device) api.Device {
	var spec *api.DeviceSpec
	if device.Spec != nil {
		spec = &device.Spec.Data
	}
	var status *api.DeviceStatus
	if device.Status != nil {
		status = &device.Status.Data
	}
	return api.Device{
		ApiVersion: &DeviceAPI,
		Kind:       &DeviceKind,
		Metadata: &api.ObjectMeta{
			Name:              util.StrToPtr(device.Name),
			CreationTimestamp: util.StrToPtr(device.CreatedAt.UTC().Format(time.RFC3339)),
		},
		Spec:   spec,
		Status: status,
	}
}

func DeviceListModelToApi(devices []model.Device) api.DeviceList {
	deviceList := make([]api.Device, len(devices))
	for i, device := range devices {
		deviceList[i] = DeviceModelToApi(device)
	}
	return api.DeviceList{
		ApiVersion: &DeviceAPI,
		Kind:       &DeviceListKind,
		Items:      deviceList,
	}
}
