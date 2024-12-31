package model

import (
	"encoding/json"
	"reflect"
	"strconv"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/samber/lo"
)

type EnrollmentRequest struct {
	Resource

	// The desired state of the enrollment request, stored as opaque JSON object.
	Spec *JSONField[api.EnrollmentRequestSpec] `gorm:"type:jsonb"`

	// The last reported state of the enrollment request, stored as opaque JSON object.
	Status *JSONField[api.EnrollmentRequestStatus] `gorm:"type:jsonb"`
}

func (e EnrollmentRequest) String() string {
	val, _ := json.Marshal(e)
	return string(val)
}

func NewEnrollmentRequestFromApiResource(resource *api.EnrollmentRequest) (*EnrollmentRequest, error) {
	if resource == nil || resource.Metadata.Name == nil {
		return &EnrollmentRequest{}, nil
	}

	status := api.EnrollmentRequestStatus{Conditions: []api.Condition{}}
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
	return &EnrollmentRequest{
		Resource: Resource{
			Name:            *resource.Metadata.Name,
			Labels:          lo.FromPtrOr(resource.Metadata.Labels, make(map[string]string)),
			Annotations:     lo.FromPtrOr(resource.Metadata.Annotations, make(map[string]string)),
			ResourceVersion: resourceVersion,
		},
		Spec:   MakeJSONField(resource.Spec),
		Status: MakeJSONField(status),
	}, nil
}

func (e *EnrollmentRequest) ToApiResource(opts ...APIResourceOption) (*api.EnrollmentRequest, error) {
	if e == nil {
		return &api.EnrollmentRequest{}, nil
	}

	spec := api.EnrollmentRequestSpec{}
	if e.Spec != nil {
		spec = e.Spec.Data
	}

	status := api.EnrollmentRequestStatus{Conditions: []api.Condition{}}
	if e.Status != nil {
		status = e.Status.Data
	}

	return &api.EnrollmentRequest{
		ApiVersion: api.EnrollmentRequestAPIVersion,
		Kind:       api.EnrollmentRequestKind,
		Metadata: api.ObjectMeta{
			Name:              util.StrToPtr(e.Name),
			CreationTimestamp: util.TimeToPtr(e.CreatedAt.UTC()),
			Labels:            lo.ToPtr(util.EnsureMap(e.Resource.Labels)),
			Annotations:       lo.ToPtr(util.EnsureMap(e.Resource.Annotations)),
			ResourceVersion:   lo.Ternary(e.ResourceVersion != nil, lo.ToPtr(strconv.FormatInt(lo.FromPtr(e.ResourceVersion), 10)), nil),
		},
		Spec:   spec,
		Status: &status,
	}, nil
}

func EnrollmentRequestsToApiResource(ers []EnrollmentRequest, cont *string, numRemaining *int64) (api.EnrollmentRequestList, error) {
	enrollmentRequestList := make([]api.EnrollmentRequest, len(ers))
	for i, enrollmentRequest := range ers {
		apiResource, _ := enrollmentRequest.ToApiResource()
		enrollmentRequestList[i] = *apiResource
	}
	ret := api.EnrollmentRequestList{
		ApiVersion: api.EnrollmentRequestAPIVersion,
		Kind:       api.EnrollmentRequestListKind,
		Items:      enrollmentRequestList,
		Metadata:   api.ListMeta{},
	}
	if cont != nil {
		ret.Metadata.Continue = cont
		ret.Metadata.RemainingItemCount = numRemaining
	}
	return ret, nil
}

func (e *EnrollmentRequest) GetKind() string {
	return api.EnrollmentRequestKind
}

func (e *EnrollmentRequest) HasNilSpec() bool {
	return e.Spec == nil
}

func (e *EnrollmentRequest) HasSameSpecAs(otherResource any) bool {
	other, ok := otherResource.(*EnrollmentRequest) // Assert that the other resource is a *EnrollmentRequest
	if !ok {
		return false // Not the same type, so specs cannot be the same
	}
	if other == nil {
		return false
	}
	if (e.Spec == nil && other.Spec != nil) || (e.Spec != nil && other.Spec == nil) {
		return false
	}
	return reflect.DeepEqual(e.Spec.Data, other.Spec.Data)
}

func (e *EnrollmentRequest) GetStatusAsJson() ([]byte, error) {
	return e.Status.MarshalJSON()
}
