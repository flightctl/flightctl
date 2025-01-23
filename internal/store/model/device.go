package model

import (
	"encoding/json"
	"strconv"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/samber/lo"
)

type Device struct {
	Resource

	// The alias for the device.
	Alias *string `selector:"metadata.alias"`

	// The desired state, stored as opaque JSON object.
	Spec *JSONField[api.DeviceSpec] `gorm:"type:jsonb"`

	// The last reported state, stored as opaque JSON object.
	Status *JSONField[api.DeviceStatus] `gorm:"type:jsonb"`

	// Conditions set by the service, as opposed to the agent.
	ServiceConditions *JSONField[ServiceConditions]

	// The rendered ignition config, exposed in a separate endpoint.
	RenderedConfig *string

	// Timestamp when the device was rendered
	RenderTimestamp time.Time

	// The rendered application provided by the service.
	RenderedApplications *JSONField[*[]api.RenderedApplicationSpec] `gorm:"type:jsonb"`

	// Join table with the relationship of devices to repositories (only maintained for standalone devices)
	Repositories []Repository `gorm:"many2many:device_repos;constraint:OnDelete:CASCADE;"`
}

type ServiceConditions struct {
	Conditions *[]api.Condition `json:"conditions,omitempty"`
}

func (d Device) String() string {
	val, _ := json.Marshal(d)
	return string(val)
}

func NewDeviceFromApiResource(resource *api.Device) (*Device, error) {
	if resource == nil || resource.Metadata.Name == nil {
		return &Device{}, nil
	}

	spec := api.DeviceSpec{}
	if resource.Spec != nil {
		spec = *resource.Spec
	}

	status := api.NewDeviceStatus()
	if resource.Status != nil {
		status = *resource.Status
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
	var alias *string
	if labels := resource.Metadata.Labels; labels != nil {
		if l, ok := (*labels)["alias"]; ok {
			alias = &l
		}
	}

	return &Device{
		Resource: Resource{
			Name:            *resource.Metadata.Name,
			Labels:          lo.FromPtrOr(resource.Metadata.Labels, make(map[string]string)),
			Annotations:     lo.FromPtrOr(resource.Metadata.Annotations, make(map[string]string)),
			Generation:      resource.Metadata.Generation,
			Owner:           resource.Metadata.Owner,
			ResourceVersion: resourceVersion,
		},
		Alias:  alias,
		Spec:   MakeJSONField(spec),
		Status: MakeJSONField(status),
	}, nil
}

func (d *Device) ToApiResource(opts ...APIResourceOption) (*api.Device, error) {
	if d == nil {
		return &api.Device{}, nil
	}

	spec := api.DeviceSpec{}
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

	var resourceVersion *string
	if d.ResourceVersion != nil {
		resourceVersion = lo.ToPtr(strconv.FormatInt(*d.ResourceVersion, 10))
	}
	return &api.Device{
		ApiVersion: api.DeviceAPIVersion,
		Kind:       api.DeviceKind,
		Metadata: api.ObjectMeta{
			Name:              util.StrToPtr(d.Name),
			CreationTimestamp: util.TimeToPtr(d.CreatedAt.UTC()),
			Labels:            lo.ToPtr(util.EnsureMap(d.Resource.Labels)),
			Annotations:       lo.ToPtr(util.EnsureMap(d.Resource.Annotations)),
			Generation:        d.Generation,
			Owner:             d.Owner,
			ResourceVersion:   resourceVersion,
		},
		Spec:   &spec,
		Status: &status,
	}, nil
}

func DevicesToApiResource(devices []Device, cont *string, numRemaining *int64) (api.DeviceList, error) {
	deviceList := make([]api.Device, len(devices))
	applicationStatuses := make(map[string]int64)
	summaryStatuses := make(map[string]int64)
	updateStatuses := make(map[string]int64)
	for i, device := range devices {
		apiResource, _ := device.ToApiResource()
		deviceList[i] = *apiResource
		applicationStatus := string(deviceList[i].Status.ApplicationsSummary.Status)
		applicationStatuses[applicationStatus] = applicationStatuses[applicationStatus] + 1
		summaryStatus := string(deviceList[i].Status.Summary.Status)
		summaryStatuses[summaryStatus] = summaryStatuses[summaryStatus] + 1
		updateStatus := string(deviceList[i].Status.Updated.Status)
		updateStatuses[updateStatus] = updateStatuses[updateStatus] + 1
	}
	ret := api.DeviceList{
		ApiVersion: api.DeviceAPIVersion,
		Kind:       api.DeviceListKind,
		Items:      deviceList,
		Metadata:   api.ListMeta{},
		Summary: &api.DevicesSummary{
			ApplicationStatus: applicationStatuses,
			SummaryStatus:     summaryStatuses,
			UpdateStatus:      updateStatuses,
			Total:             int64(len(devices)),
		},
	}
	if cont != nil {
		ret.Metadata.Continue = cont
		ret.Metadata.RemainingItemCount = numRemaining
	}
	return ret, nil
}

func (d *Device) GetKind() string {
	return api.DeviceKind
}

func (d *Device) HasNilSpec() bool {
	return d.Spec == nil
}

func (d *Device) HasSameSpecAs(otherResource any) bool {
	other, ok := otherResource.(*Device) // Assert that the other resource is a *Device
	if !ok {
		return false // Not the same type, so specs cannot be the same
	}
	if other == nil {
		return false
	}
	if (d.Spec == nil && other.Spec != nil) || (d.Spec != nil && other.Spec == nil) {
		return false
	}
	return api.DeviceSpecsAreEqual(d.Spec.Data, other.Spec.Data)
}

func (d *Device) GetStatusAsJson() ([]byte, error) {
	return d.Status.MarshalJSON()
}
