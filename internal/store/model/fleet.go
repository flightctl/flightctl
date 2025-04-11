package model

import (
	"encoding/json"
	"fmt"
	"strconv"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/util"
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

func FleetAPIVersion() string {
	return fmt.Sprintf("%s/%s", api.APIGroup, api.FleetAPIVersion)
}

func (f *Fleet) ToApiResource(opts ...APIResourceOption) (*api.Fleet, error) {
	if f == nil {
		return &api.Fleet{}, nil
	}

	options := apiResourceOptions{}
	for _, opt := range opts {
		opt(&options)
	}

	spec := api.FleetSpec{}
	if f.Spec != nil {
		spec = f.Spec.Data
	}

	status := api.FleetStatus{Conditions: []api.Condition{}}
	if f.Status != nil {
		status = f.Status.Data
	}
	status.DevicesSummary = options.devicesSummary

	return &api.Fleet{
		ApiVersion: FleetAPIVersion(),
		Kind:       api.FleetKind,
		Metadata: api.ObjectMeta{
			Name:              lo.ToPtr(f.Name),
			CreationTimestamp: lo.ToPtr(f.CreatedAt.UTC()),
			Labels:            lo.ToPtr(util.EnsureMap(f.Resource.Labels)),
			Annotations:       lo.ToPtr(util.EnsureMap(f.Resource.Annotations)),
			Generation:        f.Generation,
			Owner:             f.Owner,
			ResourceVersion:   lo.Ternary(f.ResourceVersion != nil, lo.ToPtr(strconv.FormatInt(lo.FromPtr(f.ResourceVersion), 10)), nil),
		},
		Spec:   spec,
		Status: &status,
	}, nil
}

func FleetsToApiResource(fleets []Fleet, cont *string, numRemaining *int64) (api.FleetList, error) {
	fleetList := make([]api.Fleet, len(fleets))
	for i, fleet := range fleets {
		var opts []APIResourceOption
		if fleet.Status.Data.DevicesSummary != nil {
			opts = append(opts, WithDevicesSummary(fleet.Status.Data.DevicesSummary))
		}
		apiResource, _ := fleet.ToApiResource(opts...)
		fleetList[i] = *apiResource
	}
	ret := api.FleetList{
		ApiVersion: FleetAPIVersion(),
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
