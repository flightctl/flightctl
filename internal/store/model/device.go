package model

import (
	"encoding/json"
	"strconv"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/samber/lo"
)

var (
	DeviceAPI      = "v1alpha1"
	DeviceKind     = "Device"
	DeviceListKind = "DeviceList"

	DeviceAnnotationTemplateVersion = "fleet-controller/templateVersion"
	DeviceAnnotationRenderedVersion = "device-controller/renderedVersion"
)

type Device struct {
	Resource

	// The desired state, stored as opaque JSON object.
	Spec *JSONField[api.DeviceSpec]

	// The last reported state, stored as opaque JSON object.
	Status *JSONField[api.DeviceStatus] `gorm:"type:jsonb"`

	// Conditions set by the service, as opposed to the agent.
	ServiceConditions *JSONField[ServiceConditions]

	// The rendered ignition config, exposed in a separate endpoint.
	RenderedConfig *string

	// Join table with the relationship of devices to repositories (only maintained for standalone devices)
	Repositories []Repository `gorm:"many2many:device_repos;constraint:OnDelete:CASCADE;"`
}

type ServiceConditions struct {
	Conditions *[]api.Condition `json:"conditions,omitempty"`
}

type DeviceList []Device

func (d Device) String() string {
	val, _ := json.Marshal(d)
	return string(val)
}

func NewDeviceFromApiResource(resource *api.Device) (*Device, error) {
	if resource == nil || resource.Metadata.Name == nil {
		return &Device{}, nil
	}

	var spec api.DeviceSpec
	if resource.Spec != nil {
		spec = *resource.Spec
	}

	status := api.NewDeviceStatus()
	if resource.Status != nil {
		status = *resource.Status
	}
	if status.Applications.Data == nil {
		status.Applications.Data = make(map[string]api.ApplicationStatus)
	}
	if status.Conditions == nil {
		status.Conditions = []api.Condition{}
	}
	var resourceVersion *int64
	if resource.Metadata.ResourceVersion != nil {
		i, err := strconv.ParseInt(lo.FromPtr(resource.Metadata.ResourceVersion), 10, 64)
		if err != nil {
			return nil, flterrors.ErrIllegalResourceVersionFormat
		}
		resourceVersion = &i
	}

	return &Device{
		Resource: Resource{
			Name:            *resource.Metadata.Name,
			Labels:          util.LabelMapToArray(resource.Metadata.Labels),
			Generation:      resource.Metadata.Generation,
			Owner:           resource.Metadata.Owner,
			Annotations:     util.LabelMapToArray(resource.Metadata.Annotations),
			ResourceVersion: resourceVersion,
		},
		Spec:   MakeJSONField(spec),
		Status: MakeJSONField(status),
	}, nil
}

func (d *Device) ToApiResource() api.Device {
	if d == nil {
		return api.Device{}
	}

	var spec api.DeviceSpec
	if d.Spec != nil {
		spec = d.Spec.Data
	}

	status := api.NewDeviceStatus()
	if d.Status != nil {
		status = d.Status.Data
	}

	if d.ServiceConditions != nil && d.ServiceConditions.Data.Conditions != nil {
		if status.Conditions == nil {
			status.Conditions = []api.Condition{}
		}
		status.Conditions = append(status.Conditions, *d.ServiceConditions.Data.Conditions...)
	}

	metadataLabels := util.LabelArrayToMap(d.Resource.Labels)
	metadataAnnotations := util.LabelArrayToMap(d.Resource.Annotations)

	var resourceVersion *string
	if d.ResourceVersion != nil {
		resourceVersion = lo.ToPtr(strconv.FormatInt(*d.ResourceVersion, 10))
	}
	return api.Device{
		ApiVersion: DeviceAPI,
		Kind:       DeviceKind,
		Metadata: api.ObjectMeta{
			Name:              util.StrToPtr(d.Name),
			CreationTimestamp: util.TimeToPtr(d.CreatedAt.UTC()),
			Labels:            &metadataLabels,
			Generation:        d.Generation,
			Owner:             d.Owner,
			Annotations:       &metadataAnnotations,
			ResourceVersion:   resourceVersion,
		},
		Spec:   &spec,
		Status: &status,
	}
}

func (dl DeviceList) ToApiResource(cont *string, numRemaining *int64) api.DeviceList {
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
	ret := api.DeviceList{
		ApiVersion: DeviceAPI,
		Kind:       DeviceListKind,
		Items:      deviceList,
		Metadata:   api.ListMeta{},
	}
	if cont != nil {
		ret.Metadata.Continue = cont
		ret.Metadata.RemainingItemCount = numRemaining
	}
	return ret
}
