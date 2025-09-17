package model

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
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
	ServiceConditions *JSONField[ServiceConditions] `gorm:"type:jsonb"`

	// The rendered device config
	RenderedConfig *JSONField[*[]api.ConfigProviderSpec] `gorm:"type:jsonb"`

	// Timestamp when the device was rendered
	RenderTimestamp time.Time

	// The last time the device was seen by the service
	LastSeen *time.Time `gorm:"index" selector:"lastSeen"`

	// The rendered application provided by the service.
	RenderedApplications *JSONField[*[]api.ApplicationProviderSpec] `gorm:"type:jsonb"`

	// Join table with the relationship of devices to repositories (only maintained for standalone devices)
	Repositories []Repository `gorm:"many2many:device_repos;constraint:OnDelete:CASCADE;"`
}

type DeviceLabel struct {
	OrgID      uuid.UUID `gorm:"primaryKey;type:uuid;index:,composite:device_label_org_device" selector:"metadata.orgid,hidden,private"`
	DeviceName string    `gorm:"primaryKey;index:,composite:device_label_org_device" selector:"metadata.name"`
	LabelKey   string    `gorm:"primaryKey;index:,composite:device_label_key" selector:"metadata.labels.key"`
	LabelValue string    `gorm:"index" selector:"metadata.labels.value"`

	// Foreign Key Constraint with CASCADE DELETE
	Device Device `gorm:"foreignKey:OrgID,DeviceName;references:OrgID,Name;constraint:OnDelete:CASCADE"`
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
		Alias:    alias,
		Spec:     MakeJSONField(spec),
		Status:   MakeJSONField(status),
		LastSeen: status.LastSeen,
	}, nil
}

func DeviceAPIVersion() string {
	return fmt.Sprintf("%s/%s", api.APIGroup, api.DeviceAPIVersion)
}

func (d *Device) ToApiResource(opts ...APIResourceOption) (*api.Device, error) {
	if d == nil {
		return &api.Device{}, nil
	}

	var apiOpts = &apiResourceOptions{}
	for _, opt := range opts {
		opt(apiOpts)
	}

	spec := api.DeviceSpec{}
	if d.Spec != nil {
		spec = d.Spec.Data
	}

	if apiOpts.isRendered {
		annotations := util.EnsureMap(d.Annotations)
		renderedVersion, ok := annotations[api.DeviceAnnotationRenderedVersion]
		if !ok {
			return nil, flterrors.ErrNoRenderedVersion
		}
		var consoles []api.DeviceConsole

		if val, ok := d.Annotations[api.DeviceAnnotationConsole]; ok && val != "" {
			if err := json.Unmarshal([]byte(val), &consoles); err != nil {
				return nil, fmt.Errorf("failed to unmarshal consoles: %w", err)
			}
		}

		// if we have a console request we ignore the rendered version
		// TODO: bump the rendered version instead?
		if len(consoles) == 0 && apiOpts.knownRenderedVersion != nil && renderedVersion == *apiOpts.knownRenderedVersion {
			return nil, nil
		}
		// TODO: handle multiple consoles, for now we just encapsulate our one console in a list
		spec.Config = d.RenderedConfig.Data
		spec.Applications = d.RenderedApplications.Data
		spec.Consoles = &consoles
	}

	status := api.NewDeviceStatus()
	if d.Status != nil {
		status = d.Status.Data
		if d.LastSeen != nil {
			status.LastSeen = d.LastSeen
		}
	}

	if !apiOpts.withoutServiceConditions {
		if d.ServiceConditions != nil && d.ServiceConditions.Data.Conditions != nil {
			if status.Conditions == nil {
				status.Conditions = []api.Condition{}
			}
			status.Conditions = append(status.Conditions, *d.ServiceConditions.Data.Conditions...)
		}
	}

	var resourceVersion *string
	if d.ResourceVersion != nil {
		resourceVersion = lo.ToPtr(strconv.FormatInt(*d.ResourceVersion, 10))
	}
	return &api.Device{
		ApiVersion: DeviceAPIVersion(),
		Kind:       api.DeviceKind,
		Metadata: api.ObjectMeta{
			Name:              lo.ToPtr(d.Name),
			CreationTimestamp: lo.ToPtr(d.CreatedAt.UTC()),
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
		ApiVersion: DeviceAPIVersion(),
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
