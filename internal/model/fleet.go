package model

import (
	"encoding/json"

	api "github.com/flightctl/flightctl/api/v1alpha1"
)

type Fleet struct {
	Resource

	// The desired state, stored as opaque JSON object.
	Spec *JSONField[api.FleetSpec]

	// The last reported state, stored as opaque JSON object.
	Status *JSONField[api.FleetStatus]
}

func (f Fleet) String() string {
	val, _ := json.Marshal(f)
	return string(val)
}
