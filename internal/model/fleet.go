package model

import (
	"encoding/json"
	"io"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/util"
)

var (
	FleetAPI      = "v1alpha1"
	FleetKind     = "Fleet"
	FleetListKind = "FleetList"
)

type Fleet struct {
	Resource

	// The desired state, stored as opaque JSON object.
	Spec *JSONField[api.FleetSpec]

	// The last reported state, stored as opaque JSON object.
	Status *JSONField[api.FleetStatus]
}

type FleetList []Fleet

func (d Fleet) String() string {
	val, _ := json.Marshal(d)
	return string(val)
}

func NewFleetFromApiResourceReader(r io.Reader) (*Fleet, error) {
	var res api.Fleet
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&res)
	if err != nil {
		return nil, err
	}
	return NewFleetFromApiResource(&res), nil
}

func NewFleetFromApiResource(res *api.Fleet) *Fleet {
	spec := api.FleetSpec{}
	status := api.FleetStatus{}
	if res.Spec != nil {
		spec = api.FleetSpec(*res.Spec)
	}
	if res.Status != nil {
		status = api.FleetStatus(*res.Status)
	}
	return &Fleet{
		Resource: Resource{
			Name: res.Metadata.Name,
		},
		Spec:   MakeJSONField(spec),
		Status: MakeJSONField(status),
	}
}

func (d *Fleet) ToApiResource() api.Fleet {
	if d == nil {
		return api.Fleet{}
	}

	var spec *api.FleetSpec
	if d.Spec != nil {
		spec = &d.Spec.Data
	}
	var status *api.FleetStatus
	if d.Status != nil {
		status = &d.Status.Data
	}
	return api.Fleet{
		ApiVersion: FleetAPI,
		Kind:       FleetKind,
		Metadata: api.ObjectMeta{
			Name:              d.Name,
			CreationTimestamp: util.StrToPtr(d.CreatedAt.UTC().Format(time.RFC3339)),
		},
		Spec:   spec,
		Status: status,
	}
}

func (dl FleetList) ToApiResource() api.FleetList {
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
	return api.FleetList{
		ApiVersion: FleetAPI,
		Kind:       FleetListKind,
		Items:      fleetList,
	}
}
