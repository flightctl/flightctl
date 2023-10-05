package model

import (
	"encoding/json"

	api "github.com/flightctl/flightctl/api/v1alpha1"
)

type EnrollmentRequest struct {
	Resource

	// The desired state of the enrollment request, stored as opaque JSON object.
	Spec *JSONField[api.EnrollmentRequestSpec]

	// The last reported state of the enrollment request, stored as opaque JSON object.
	Status *JSONField[api.EnrollmentRequestStatus]
}

func (e EnrollmentRequest) String() string {
	val, _ := json.Marshal(e)
	return string(val)
}
