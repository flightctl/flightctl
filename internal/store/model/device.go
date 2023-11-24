package model

import (
	"encoding/json"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/util"
)

var (
	DeviceAPI      = "v1alpha1"
	DeviceKind     = "Device"
	DeviceListKind = "DeviceList"
)

type Device struct {
	Resource

	// The desired state, stored as opaque JSON object.
	Spec *JSONField[api.DeviceSpec]

	// The last reported state, stored as opaque JSON object.
	Status *JSONField[api.DeviceStatus]
}

type DeviceList []Device

func (d Device) String() string {
	val, _ := json.Marshal(d)
	return string(val)
}

func NewDeviceFromApiResource(res *api.Device) *Device {
	spec := api.DeviceSpec{}
	status := api.DeviceStatus{}
	if res.Spec != nil {
		spec = api.DeviceSpec(*res.Spec)
	}
	if res.Status != nil {
		status = api.DeviceStatus(*res.Status)
	}
	return &Device{
		Resource: Resource{
			Name: res.Metadata.Name,
		},
		Spec:   MakeJSONField(spec),
		Status: MakeJSONField(status),
	}
}

func (d *Device) ToApiResource() api.Device {
	if d == nil {
		return api.Device{}
	}

	var spec *api.DeviceSpec
	if d.Spec != nil {
		spec = &d.Spec.Data
	}
	var status *api.DeviceStatus
	if d.Status != nil {
		status = &d.Status.Data
	}
	return api.Device{
		ApiVersion: DeviceAPI,
		Kind:       DeviceKind,
		Metadata: api.ObjectMeta{
			Name:              d.Name,
			CreationTimestamp: util.StrToPtr(d.CreatedAt.UTC().Format(time.RFC3339)),
		},
		Spec:   spec,
		Status: status,
	}
}

func (dl DeviceList) ToApiResource() api.DeviceList {
	if dl == nil {
		return api.DeviceList{
			ApiVersion: DeviceAPI,
			Kind:       DeviceListKind,
			Items:      []api.Device{},
		}
	}

	deviceList := make([]api.Device, len(dl))
	for i, device := range dl {
		deviceList[i] = device.ToApiResource()
	}
	return api.DeviceList{
		ApiVersion: DeviceAPI,
		Kind:       DeviceListKind,
		Items:      deviceList,
	}
}
