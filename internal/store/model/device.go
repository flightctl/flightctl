package model

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
)

type Device struct {
	Resource

	// The alias for the device.
	Alias *string `selector:"metadata.alias"`

	// The desired state, stored as opaque JSON object.
	Spec *JSONField[domain.DeviceSpec] `gorm:"type:jsonb"`

	// The last reported state, stored as opaque JSON object.
	Status *JSONField[domain.DeviceStatus] `gorm:"type:jsonb"`

	// Conditions set by the service, as opposed to the agent.
	ServiceConditions *JSONField[ServiceConditions] `gorm:"type:jsonb"`

	// Encrypted at rest as a whole blob. The existing DB column is jsonb;
	// keeping the tag prevents GORM from attempting a column type migration.
	RenderedConfig *JSONField[json.RawMessage] `gorm:"type:jsonb"`

	// Timestamp when the device was rendered
	RenderTimestamp time.Time

	// Encrypted at rest as a whole blob (see RenderedConfig).
	RenderedApplications *JSONField[json.RawMessage] `gorm:"type:jsonb"`

	// Join table with the relationship of devices to repositories (only maintained for standalone devices)
	Repositories []Repository `gorm:"many2many:device_repos;constraint:OnDelete:CASCADE;"`

	DeviceTimestamp DeviceTimestamp `gorm:"foreignKey:OrgID,Name;references:OrgID,Name;constraint:OnDelete:CASCADE"`
}

type DeviceTimestamp struct {
	OrgID uuid.UUID `gorm:"type:uuid;primaryKey;index:,composite:org_name,priority:1"`
	// Uniquely identifies the resource within an organization and schema.
	// Depending on the schema (kind), assigned by the device management system or the crypto identity of the device (public key). Immutable.
	// This may become a URN later, so it's important API users treat this as an opaque handle.
	Name string `gorm:"primaryKey;index:,composite:org_name,priority:2" selector:"metadata.name"`

	// The last time the device was seen (reported status).  Since this is updated frequently,
	// we store it in a separate table to avoid bloating the main device table.  In addition we don't index this field
	// because the effort of maintaining the index outweighs the benefit of querying by last seen.
	LastSeen *time.Time
}

type DeviceWithTimestamp struct {
	Device
	LastSeen *time.Time
}

type DeviceType interface {
	Device | DeviceWithTimestamp
}

type DeviceTypePtr interface {
	ToApiResource(opts ...APIResourceOption) (*domain.Device, error)
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
	Conditions     *[]domain.Condition          `json:"conditions,omitempty"`
	DependencySync *domain.DependencySyncStatus `json:"dependencySync,omitempty"`
}

func (d Device) String() string {
	val, _ := json.Marshal(d)
	return string(val)
}

func NewDeviceFromApiResource(resource *domain.Device) (*Device, error) {
	if resource == nil || resource.Metadata.Name == nil {
		return &Device{}, nil
	}

	spec := domain.DeviceSpec{}
	if resource.Spec != nil {
		spec = *resource.Spec
	}

	status := domain.NewDeviceStatus()
	if resource.Status != nil {
		status = *resource.Status
	}
	if status.Conditions == nil {
		status.Conditions = []domain.Condition{}
	}
	var serviceConditions ServiceConditions
	serviceConditions.Conditions = lo.ToPtr(lo.Filter(status.Conditions, func(c domain.Condition, _ int) bool {
		return c.Type.IsServiceConditionType()
	}))

	status.Conditions = lo.Filter(status.Conditions, func(c domain.Condition, _ int) bool {
		return !c.Type.IsServiceConditionType()
	})

	if status.DependencySync != nil {
		serviceConditions.DependencySync = status.DependencySync
		status.DependencySync = nil
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
		Alias:             alias,
		Spec:              MakeJSONField(spec),
		Status:            MakeJSONField(status),
		ServiceConditions: MakeJSONField(serviceConditions),
	}, nil
}

func DeviceAPIVersion() string {
	return fmt.Sprintf("%s/%s", domain.APIGroup, domain.DeviceAPIVersion)
}

// decryptRenderedField decrypts a rendered config or applications field stored
// as jsonb. The RawMessage holds the raw JSON token: a quoted string for
// encrypted data ("enc:v1:default:...") or a bare array for legacy plaintext.
func decryptRenderedField(ctx context.Context, field *JSONField[json.RawMessage]) ([]byte, error) {
	if field == nil || len(field.Data) == 0 {
		return nil, nil
	}
	raw := []byte(field.Data)
	// Encrypted: RawMessage is a JSON string "enc:v1:default:...".
	// Unmarshal extracts the inner string for decryption.
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		decrypted, _, err := encryption.Decrypt(ctx, encryption.Ciphertext(str))
		return decrypted, err
	}
	// BC: unencrypted legacy data is a bare JSON array, e.g. [{...}]
	return raw, nil
}

func (d *Device) ToApiResource(opts ...APIResourceOption) (*domain.Device, error) {
	if d == nil {
		return &domain.Device{}, nil
	}

	var apiOpts = &apiResourceOptions{}
	for _, opt := range opts {
		opt(apiOpts)
	}

	spec := domain.DeviceSpec{}
	if d.Spec != nil {
		spec = d.Spec.Data
	}

	if apiOpts.isRendered {
		ctx := apiOpts.ctx
		if ctx == nil {
			ctx = context.Background()
		}

		annotations := util.EnsureMap(d.Annotations)
		renderedVersion, ok := annotations[domain.DeviceAnnotationRenderedVersion]
		if !ok {
			return nil, flterrors.ErrNoRenderedVersion
		}
		consoles := []domain.DeviceConsole{}

		if val, ok := d.Annotations[domain.DeviceAnnotationConsole]; ok && val != "" {
			if err := json.Unmarshal([]byte(val), &consoles); err != nil {
				return nil, fmt.Errorf("failed to unmarshal consoles: %w", err)
			}
		}

		// if we have a console request we ignore the rendered version
		// TODO: bump the rendered version instead?
		if len(consoles) == 0 && apiOpts.knownRenderedVersion != nil {
			// Try numeric comparison first, fall back to lexicographic if parsing fails
			renderedVersionInt, err1 := strconv.ParseInt(renderedVersion, 10, 64)
			knownRenderedVersionInt, err2 := strconv.ParseInt(*apiOpts.knownRenderedVersion, 10, 64)

			if err1 == nil && err2 == nil {
				// Both versions are numeric, use numeric comparison
				if renderedVersionInt <= knownRenderedVersionInt {
					return nil, nil
				}
			} else {
				// Fall back to lexicographic comparison for non-numeric versions
				if renderedVersion <= *apiOpts.knownRenderedVersion {
					return nil, nil
				}
			}
		}
		// TODO: handle multiple consoles, for now we just encapsulate our one console in a list
		spec.Consoles = &consoles

		if data, err := decryptRenderedField(ctx, d.RenderedConfig); err != nil {
			return nil, fmt.Errorf("decrypt rendered config: %w", err)
		} else if data != nil {
			var config []domain.ConfigProviderSpec
			if err := json.Unmarshal(data, &config); err != nil {
				return nil, fmt.Errorf("unmarshal rendered config: %w", err)
			}
			spec.Config = &config
		}
		if data, err := decryptRenderedField(ctx, d.RenderedApplications); err != nil {
			return nil, fmt.Errorf("decrypt rendered applications: %w", err)
		} else if data != nil {
			var apps []domain.ApplicationProviderSpec
			if err := json.Unmarshal(data, &apps); err != nil {
				return nil, fmt.Errorf("unmarshal rendered applications: %w", err)
			}
			spec.Applications = &apps
		}
	}

	status := domain.NewDeviceStatus()
	if d.Status != nil {
		status = d.Status.Data
	}

	if d.ServiceConditions != nil {
		if d.ServiceConditions.Data.Conditions != nil {
			if status.Conditions == nil {
				status.Conditions = []domain.Condition{}
			}
			status.Conditions = append(status.Conditions, *d.ServiceConditions.Data.Conditions...)
		}
		if d.ServiceConditions.Data.DependencySync != nil {
			status.DependencySync = d.ServiceConditions.Data.DependencySync
		}
	}

	var resourceVersion *string
	if d.ResourceVersion != nil {
		resourceVersion = lo.ToPtr(strconv.FormatInt(*d.ResourceVersion, 10))
	}
	return &domain.Device{
		ApiVersion: DeviceAPIVersion(),
		Kind:       domain.DeviceKind,
		Metadata: domain.ObjectMeta{
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

func DevicesToApiResource[D DeviceType](devices []D, cont *string, numRemaining *int64) (domain.DeviceList, error) {
	deviceList := make([]domain.Device, len(devices))
	applicationStatuses := make(map[string]int64)
	summaryStatuses := make(map[string]int64)
	updateStatuses := make(map[string]int64)
	for i, device := range devices {
		dptr, ok := any(&device).(DeviceTypePtr)
		if !ok {
			return domain.DeviceList{}, fmt.Errorf("type assertion to DeviceTypePtr failed")
		}
		apiResource, _ := dptr.ToApiResource()
		deviceList[i] = *apiResource
		applicationStatus := string(deviceList[i].Status.ApplicationsSummary.Status)
		applicationStatuses[applicationStatus] = applicationStatuses[applicationStatus] + 1
		summaryStatus := string(deviceList[i].Status.Summary.Status)
		summaryStatuses[summaryStatus] = summaryStatuses[summaryStatus] + 1
		updateStatus := string(deviceList[i].Status.Updated.Status)
		updateStatuses[updateStatus] = updateStatuses[updateStatus] + 1
	}
	ret := domain.DeviceList{
		ApiVersion: DeviceAPIVersion(),
		Kind:       domain.DeviceListKind,
		Items:      deviceList,
		Metadata:   domain.ListMeta{},
		Summary: &domain.DevicesSummary{
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
	return domain.DeviceKind
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
	if d.Spec == nil && other.Spec == nil {
		return true
	}
	if (d.Spec == nil && other.Spec != nil) || (d.Spec != nil && other.Spec == nil) {
		return false
	}
	return domain.DeviceSpecsAreEqual(d.Spec.Data, other.Spec.Data)
}

func (d *Device) GetStatusAsJson() ([]byte, error) {
	return d.Status.MarshalJSON()
}

func (d *DeviceWithTimestamp) ToApiResource(opts ...APIResourceOption) (*domain.Device, error) {
	if d == nil {
		return &domain.Device{}, nil
	}
	baseDevice, err := d.Device.ToApiResource(opts...)
	if err != nil {
		return nil, err
	}
	if d.LastSeen != nil {
		baseDevice.Status.LastSeen = lo.ToPtr(d.LastSeen.UTC())
	}
	return baseDevice, nil
}
