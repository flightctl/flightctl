package model

import (
	"encoding/json"
	"reflect"
	"strconv"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
)

type EnrollmentRequest struct {
	Resource

	// The desired state of the enrollment request, stored as opaque JSON object.
	Spec *JSONField[api.EnrollmentRequestSpec] `gorm:"type:jsonb"`

	// The last reported state of the enrollment request, stored as opaque JSON object.
	Status *JSONField[api.EnrollmentRequestStatus] `gorm:"type:jsonb"`
}

type EnrollmentRequestList []EnrollmentRequest

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
		Spec:   e.Spec.Data,
		Status: &status,
	}, nil
}

func (el *EnrollmentRequestList) ToApiResource(cont *string, numRemaining *int64) (api.EnrollmentRequestList, error) {
	if el == nil {
		return api.EnrollmentRequestList{
			ApiVersion: api.EnrollmentRequestAPIVersion,
			Kind:       api.EnrollmentRequestListKind,
			Items:      []api.EnrollmentRequest{},
		}, nil
	}

	enrollmentRequestList := make([]api.EnrollmentRequest, len(*el))
	for i, enrollmentRequest := range *el {
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

func EnrollmentRequestPtrReturnSelf(e *EnrollmentRequest) *EnrollmentRequest {
	return e
}

func (e *EnrollmentRequest) GetKind() string {
	return api.EnrollmentRequestKind
}

func (e *EnrollmentRequest) GetName() string {
	return e.Name
}

func (e *EnrollmentRequest) GetOrgID() uuid.UUID {
	return e.OrgID
}

func (e *EnrollmentRequest) SetOrgID(orgId uuid.UUID) {
	e.OrgID = orgId
}

func (e *EnrollmentRequest) GetResourceVersion() *int64 {
	return e.ResourceVersion
}

func (e *EnrollmentRequest) SetResourceVersion(version *int64) {
	e.ResourceVersion = version
}

func (e *EnrollmentRequest) GetGeneration() *int64 {
	return e.Generation
}

func (e *EnrollmentRequest) SetGeneration(generation *int64) {
	e.Generation = generation
}

func (e *EnrollmentRequest) GetOwner() *string {
	return e.Owner
}

func (e *EnrollmentRequest) SetOwner(owner *string) {
	e.Owner = owner
}

func (e *EnrollmentRequest) GetLabels() JSONMap[string, string] {
	return e.Labels
}

func (e *EnrollmentRequest) SetLabels(labels JSONMap[string, string]) {
	e.Labels = labels
}

func (e *EnrollmentRequest) GetAnnotations() JSONMap[string, string] {
	return e.Annotations
}

func (e *EnrollmentRequest) SetAnnotations(annotations JSONMap[string, string]) {
	e.Annotations = annotations
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

func (el *EnrollmentRequestList) Length() int {
	return len(*el)
}

func (el *EnrollmentRequestList) GetItem(i int) Generic {
	return &((*el)[i])
}

func (el *EnrollmentRequestList) RemoveLast() {
	*el = (*el)[:len(*el)-1]
}
