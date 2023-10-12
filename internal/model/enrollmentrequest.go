package model

import (
	"encoding/json"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/util"
)

var (
	EnrollmentRequestAPI      = "v1alpha1"
	EnrollmentRequestKind     = "EnrollmentRequest"
	EnrollmentRequestListKind = "EnrollmentRequestList"
)

type EnrollmentRequest struct {
	Resource

	// The desired state of the enrollment request, stored as opaque JSON object.
	Spec *JSONField[api.EnrollmentRequestSpec]

	// The last reported state of the enrollment request, stored as opaque JSON object.
	Status *JSONField[api.EnrollmentRequestStatus]
}

type EnrollmentRequestList []EnrollmentRequest

func (e EnrollmentRequest) String() string {
	val, _ := json.Marshal(e)
	return string(val)
}

func NewEnrollmentRequestFromApiResource(res *api.EnrollmentRequest) *EnrollmentRequest {
	spec := api.EnrollmentRequestSpec{}
	status := api.EnrollmentRequestStatus{}
	if res.Spec != nil {
		spec = api.EnrollmentRequestSpec(*res.Spec)
	}
	if res.Status != nil {
		status = api.EnrollmentRequestStatus(*res.Status)
	}
	return &EnrollmentRequest{
		Resource: Resource{
			Name: res.Metadata.Name,
		},
		Spec:   MakeJSONField(spec),
		Status: MakeJSONField(status),
	}
}

func (e *EnrollmentRequest) ToApiResource() api.EnrollmentRequest {
	if e == nil {
		return api.EnrollmentRequest{}
	}

	var spec *api.EnrollmentRequestSpec
	if e.Spec != nil {
		spec = &e.Spec.Data
	}
	var status *api.EnrollmentRequestStatus
	if e.Status != nil {
		status = &e.Status.Data
	}
	return api.EnrollmentRequest{
		ApiVersion: EnrollmentRequestAPI,
		Kind:       EnrollmentRequestKind,
		Metadata: api.ObjectMeta{
			Name:              e.Name,
			CreationTimestamp: util.StrToPtr(e.CreatedAt.UTC().Format(time.RFC3339)),
		},
		Spec:   spec,
		Status: status,
	}
}

func (el EnrollmentRequestList) ToApiResource() api.EnrollmentRequestList {
	if el == nil {
		return api.EnrollmentRequestList{
			ApiVersion: EnrollmentRequestAPI,
			Kind:       EnrollmentRequestListKind,
			Items:      []api.EnrollmentRequest{},
		}
	}

	EnrollmentRequestList := make([]api.EnrollmentRequest, len(el))
	for i, EnrollmentRequest := range el {
		EnrollmentRequestList[i] = EnrollmentRequest.ToApiResource()
	}
	return api.EnrollmentRequestList{
		ApiVersion: EnrollmentRequestAPI,
		Kind:       EnrollmentRequestListKind,
		Items:      EnrollmentRequestList,
	}
}
