package model

import (
	"encoding/json"
	"strconv"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
)

type Fleet struct {
	Resource

	// The desired state, stored as opaque JSON object.
	Spec *JSONField[api.FleetSpec] `gorm:"type:jsonb"`

	// The last reported state, stored as opaque JSON object.
	Status *JSONField[api.FleetStatus] `gorm:"type:jsonb"`

	// Join table with the relationship of fleets to repositories
	Repositories []Repository `gorm:"many2many:fleet_repos;constraint:OnDelete:CASCADE;"`
}

type FleetList []Fleet

func (d Fleet) String() string {
	val, _ := json.Marshal(d)
	return string(val)
}

func NewFleetFromApiResource(resource *api.Fleet) (*Fleet, error) {
	if resource == nil || resource.Metadata.Name == nil {
		return &Fleet{}, nil
	}

	status := api.FleetStatus{Conditions: []api.Condition{}}
	if resource.Status != nil {
		status = *resource.Status
	}
	var resourceVersion *int64
	if resource.Metadata.ResourceVersion != nil {
		i, err := strconv.ParseInt(lo.FromPtr(resource.Metadata.ResourceVersion), 10, 64)
		if err != nil {
			return nil, flterrors.ErrIllegalResourceVersionFormat
		}
		resourceVersion = &i
	}
	return &Fleet{
		Resource: Resource{
			Name:            *resource.Metadata.Name,
			Labels:          lo.FromPtrOr(resource.Metadata.Labels, make(map[string]string)),
			Annotations:     lo.FromPtrOr(resource.Metadata.Annotations, make(map[string]string)),
			Generation:      resource.Metadata.Generation,
			Owner:           resource.Metadata.Owner,
			ResourceVersion: resourceVersion,
		},
		Spec:   MakeJSONField(resource.Spec),
		Status: MakeJSONField(status),
	}, nil
}

func (f *Fleet) ToApiResource(opts ...APIResourceOption) (*api.Fleet, error) {
	if f == nil {
		return &api.Fleet{}, nil
	}

	options := apiResourceOptions{}
	for _, opt := range opts {
		opt(&options)
	}

	status := api.FleetStatus{Conditions: []api.Condition{}}
	if f.Status != nil {
		status = f.Status.Data
	}
	status.DevicesSummary = options.devicesSummary

	return &api.Fleet{
		ApiVersion: api.FleetAPIVersion,
		Kind:       api.FleetKind,
		Metadata: api.ObjectMeta{
			Name:              util.StrToPtr(f.Name),
			CreationTimestamp: util.TimeToPtr(f.CreatedAt.UTC()),
			Labels:            lo.ToPtr(util.EnsureMap(f.Resource.Labels)),
			Annotations:       lo.ToPtr(util.EnsureMap(f.Resource.Annotations)),
			Generation:        f.Generation,
			Owner:             f.Owner,
			ResourceVersion:   lo.Ternary(f.ResourceVersion != nil, lo.ToPtr(strconv.FormatInt(lo.FromPtr(f.ResourceVersion), 10)), nil),
		},
		Spec:   f.Spec.Data,
		Status: &status,
	}, nil
}

func (fl FleetList) ToApiResource(cont *string, numRemaining *int64) (api.FleetList, error) {
	if fl == nil {
		return api.FleetList{
			ApiVersion: api.FleetAPIVersion,
			Kind:       api.FleetListKind,
			Items:      []api.Fleet{},
		}, nil
	}

	fleetList := make([]api.Fleet, len(fl))
	for i, fleet := range fl {
		var opts []APIResourceOption
		if fleet.Status.Data.DevicesSummary != nil {
			opts = append(opts, WithDevicesSummary(fleet.Status.Data.DevicesSummary))
		}
		apiResource, _ := fleet.ToApiResource(opts...)
		fleetList[i] = *apiResource
	}
	ret := api.FleetList{
		ApiVersion: api.FleetAPIVersion,
		Kind:       api.FleetListKind,
		Items:      fleetList,
		Metadata:   api.ListMeta{},
	}
	if cont != nil {
		ret.Metadata.Continue = cont
		ret.Metadata.RemainingItemCount = numRemaining
	}
	return ret, nil
}

func FleetPtrToFleet(f *Fleet) *Fleet {
	return f
}

func (f *Fleet) GetKind() string {
	return api.FleetKind
}

func (f *Fleet) GetName() string {
	return f.Name
}

func (f *Fleet) GetOrgID() uuid.UUID {
	return f.OrgID
}

func (f *Fleet) SetOrgID(orgId uuid.UUID) {
	f.OrgID = orgId
}

func (f *Fleet) GetResourceVersion() *int64 {
	return f.ResourceVersion
}

func (f *Fleet) SetResourceVersion(version *int64) {
	f.ResourceVersion = version
}

func (f *Fleet) GetGeneration() *int64 {
	return f.Generation
}

func (f *Fleet) SetGeneration(generation *int64) {
	f.Generation = generation
}

func (f *Fleet) GetOwner() *string {
	return f.Owner
}

func (f *Fleet) SetOwner(owner *string) {
	f.Owner = owner
}

func (f *Fleet) GetLabels() JSONMap[string, string] {
	return f.Labels
}

func (f *Fleet) SetLabels(labels JSONMap[string, string]) {
	f.Labels = labels
}

func (f *Fleet) GetAnnotations() JSONMap[string, string] {
	return f.Annotations
}

func (f *Fleet) SetAnnotations(annotations JSONMap[string, string]) {
	f.Annotations = annotations
}

func (f *Fleet) HasNilSpec() bool {
	return f.Spec == nil
}

func (f *Fleet) HasSameSpecAs(otherResource any) bool {
	other, ok := otherResource.(*Fleet) // Assert that the other resource is a *Fleet
	if !ok {
		return false // Not the same type, so specs cannot be the same
	}
	if other == nil {
		return false
	}
	if (f.Spec == nil && other.Spec != nil) || (f.Spec != nil && other.Spec == nil) {
		return false
	}
	return api.FleetSpecsAreEqual(f.Spec.Data, other.Spec.Data)
}

func (f *Fleet) GetStatusAsJson() ([]byte, error) {
	return f.Status.MarshalJSON()
}

func (fl *FleetList) Length() int {
	return len(*fl)
}

func (fl *FleetList) GetItem(i int) Generic {
	return &((*fl)[i])
}

func (fl *FleetList) RemoveLast() {
	*fl = (*fl)[:len(*fl)-1]
}
