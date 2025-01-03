package model

import (
	"encoding/json"
	"strconv"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/lib/pq"
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

	// The rendered application provided by the service.
	RenderedApplications *JSONField[*[]api.RenderedApplicationSpec] `gorm:"type:jsonb"`

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
			Labels:          util.LabelMapToArray(resource.Metadata.Labels),
			Generation:      resource.Metadata.Generation,
			Owner:           resource.Metadata.Owner,
			Annotations:     util.LabelMapToArray(resource.Metadata.Annotations),
			ResourceVersion: resourceVersion,
		},
		Alias:  alias,
		Spec:   MakeJSONField(spec),
		Status: MakeJSONField(status),
	}, nil
}

func (d *Device) ToApiResource(opts ...APIResourceOption) *api.Device {
	if d == nil {
		return &api.Device{}
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

	metadataLabels := util.LabelArrayToMap(d.Resource.Labels)
	metadataAnnotations := util.LabelArrayToMap(d.Resource.Annotations)

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
			ApiVersion: api.DeviceAPIVersion,
			Kind:       api.DeviceListKind,
			Items:      []api.Device{},
		}
	}

	deviceList := make([]api.Device, len(dl))
	applicationStatuses := make(map[string]int64)
	summaryStatuses := make(map[string]int64)
	updateStatuses := make(map[string]int64)
	for i, device := range dl {
		deviceList[i] = *device.ToApiResource()
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
			Total:             int64(len(dl)),
		},
	}
	if cont != nil {
		ret.Metadata.Continue = cont
		ret.Metadata.RemainingItemCount = numRemaining
	}
	return ret
}

func DevicePtrToDevice(p *Device) *Device {
	return p
}

func (d *Device) GetKind() string {
	return api.DeviceKind
}

func (d *Device) GetName() string {
	return d.Name
}

func (d *Device) GetOrgID() uuid.UUID {
	return d.OrgID
}

func (d *Device) SetOrgID(orgId uuid.UUID) {
	d.OrgID = orgId
}

func (d *Device) GetResourceVersion() *int64 {
	return d.ResourceVersion
}

func (d *Device) SetResourceVersion(version *int64) {
	d.ResourceVersion = version
}

func (d *Device) GetGeneration() *int64 {
	return d.Generation
}

func (d *Device) SetGeneration(generation *int64) {
	d.Generation = generation
}

func (d *Device) GetOwner() *string {
	return d.Owner
}

func (d *Device) SetOwner(owner *string) {
	d.Owner = owner
}

func (d *Device) GetLabels() pq.StringArray {
	return d.Labels
}

func (d *Device) SetLabels(labels pq.StringArray) {
	d.Labels = labels
}

func (d *Device) GetAnnotations() pq.StringArray {
	return d.Annotations
}

func (d *Device) SetAnnotations(annotations pq.StringArray) {
	d.Annotations = annotations
}

func (d *Device) HasSameSpecAs(otherResource any) bool {
	otherDev, ok := otherResource.(*Device) // Assert that the other resource is a *Device
	if !ok {
		return false // Not the same type, so specs cannot be the same
	}
	if otherDev == nil || otherDev.Spec == nil {
		return false
	}
	return api.DeviceSpecsAreEqual(d.Spec.Data, otherDev.Spec.Data)
}
