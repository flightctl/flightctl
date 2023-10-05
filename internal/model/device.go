package model

import (
	"encoding/json"

	api "github.com/flightctl/flightctl/api/v1alpha1"
)

type Device struct {
	Resource

	// The desired state, stored as opaque JSON object.
	Spec *JSONField[api.DeviceSpec]

	// The last reported state, stored as opaque JSON object.
	Status *JSONField[api.DeviceStatus]
}

func (d Device) String() string {
	val, _ := json.Marshal(d)
	return string(val)
}
