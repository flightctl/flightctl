package model

import (
	"encoding/json"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/util"
)

var (
	FleetAPI      = "v1alpha1"
	FleetKind     = "Fleet"
	FleetListKind = "FleetList"

	FleetAnnotationTemplateVersion = "fleet-controller/templateVersion"
)

type Fleet struct {
	Resource

	// The desired state, stored as opaque JSON object.
	Spec *JSONField[api.FleetSpec]

	// The last reported state, stored as opaque JSON object.
	Status *JSONField[api.FleetStatus]

	// Join table with the relationship of fleets to repositories
	Repositories []Repository `gorm:"many2many:fleet_repos;constraint:OnDelete:CASCADE;"`
}

type FleetList []Fleet

func (d Fleet) String() string {
	val, _ := json.Marshal(d)
	return string(val)
}

func NewFleetFromApiResource(resource *api.Fleet) *Fleet {
	if resource == nil || resource.Metadata.Name == nil {
		return &Fleet{}
	}

	status := api.FleetStatus{Conditions: []api.Condition{}}
	if resource.Status != nil {
		status = *resource.Status
	}
	return &Fleet{
		Resource: Resource{
			Name:        *resource.Metadata.Name,
			Labels:      util.LabelMapToArray(resource.Metadata.Labels),
			Generation:  resource.Metadata.Generation,
			Owner:       resource.Metadata.Owner,
			Annotations: util.LabelMapToArray(resource.Metadata.Annotations),
		},
		Spec:   MakeJSONField(resource.Spec),
		Status: MakeJSONField(status),
	}
}

func (f *Fleet) ToApiResource() api.Fleet {
	if f == nil {
		return api.Fleet{}
	}

	status := api.FleetStatus{Conditions: []api.Condition{}}
	if f.Status != nil {
		status = f.Status.Data
	}

	metadataLabels := util.LabelArrayToMap(f.Resource.Labels)
	metadataAnnotations := util.LabelArrayToMap(f.Resource.Annotations)

	return api.Fleet{
		ApiVersion: FleetAPI,
		Kind:       FleetKind,
		Metadata: api.ObjectMeta{
			Name:              util.StrToPtr(f.Name),
			CreationTimestamp: util.TimeToPtr(f.CreatedAt.UTC()),
			Labels:            &metadataLabels,
			Generation:        f.Generation,
			Owner:             f.Owner,
			Annotations:       &metadataAnnotations,
			ResourceVersion:   GetResourceVersion(f.UpdatedAt),
		},
		Spec:   f.Spec.Data,
		Status: &status,
	}
}

func (dl FleetList) ToApiResource(cont *string, numRemaining *int64) api.FleetList {
	if dl == nil {
		return api.FleetList{
			ApiVersion: FleetAPI,
			Kind:       FleetListKind,
			Items:      []api.Fleet{},
		}
	}

	fleetList := make([]api.Fleet, len(dl))
	for i, fleet := range dl {
		fleetList[i] = fleet.ToApiResource()
	}
	ret := api.FleetList{
		ApiVersion: FleetAPI,
		Kind:       FleetListKind,
		Items:      fleetList,
		Metadata:   api.ListMeta{},
	}
	if cont != nil {
		ret.Metadata.Continue = cont
		ret.Metadata.RemainingItemCount = numRemaining
	}
	return ret
}
