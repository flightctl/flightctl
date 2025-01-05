package model

import (
	"encoding/json"
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

type APIResourceOption func(*apiResourceOptions)

type apiResourceOptions struct {
	summary *api.DevicesSummary
}

func WithSummary(summary *api.DevicesSummary) APIResourceOption {
	return func(o *apiResourceOptions) {
		o.summary = summary
	}
}

func (f *Fleet) ToApiResource(opts ...APIResourceOption) api.Fleet {
	if f == nil {
		return api.Fleet{}
	}

	options := apiResourceOptions{}
	for _, opt := range opts {
		opt(&options)
	}

	status := api.FleetStatus{Conditions: []api.Condition{}}
	if f.Status != nil {
		status = f.Status.Data
	}
	status.DevicesSummary = options.summary

	return api.Fleet{
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
	}
}

func (dl FleetList) ToApiResource(cont *string, numRemaining *int64) api.FleetList {
	if dl == nil {
		return api.FleetList{
			ApiVersion: api.FleetAPIVersion,
			Kind:       api.FleetListKind,
			Items:      []api.Fleet{},
		}
	}

	fleetList := make([]api.Fleet, len(dl))
	for i, fleet := range dl {
		var opts []APIResourceOption
		if fleet.Status.Data.DevicesSummary != nil {
			opts = append(opts, WithSummary(fleet.Status.Data.DevicesSummary))
		}
		fleetList[i] = fleet.ToApiResource(opts...)
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
	return ret
}
