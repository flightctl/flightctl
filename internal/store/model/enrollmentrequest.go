package model

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/samber/lo"
)

type EnrollmentRequest struct {
	Resource

	// The desired state of the enrollment request, stored as opaque JSON object.
	Spec *JSONField[domain.EnrollmentRequestSpec] `gorm:"type:jsonb"`

	// The last reported state of the enrollment request, stored as opaque JSON object.
	Status *JSONField[domain.EnrollmentRequestStatus] `gorm:"type:jsonb"`
}

func (e EnrollmentRequest) String() string {
	val, _ := json.Marshal(e)
	return string(val)
}

func NewEnrollmentRequestFromApiResource(resource *domain.EnrollmentRequest) (*EnrollmentRequest, error) {
	if resource == nil || resource.Metadata.Name == nil {
		return &EnrollmentRequest{}, nil
	}

	status := domain.EnrollmentRequestStatus{Conditions: []domain.Condition{}}
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

func EnrollmentRequestAPIVersion() string {
	return fmt.Sprintf("%s/%s", domain.APIGroup, domain.EnrollmentRequestAPIVersion)
}

func (e *EnrollmentRequest) ToApiResource(opts ...APIResourceOption) (*domain.EnrollmentRequest, error) {
	if e == nil {
		return &domain.EnrollmentRequest{}, nil
	}

	spec := domain.EnrollmentRequestSpec{}
	if e.Spec != nil {
		spec = e.Spec.Data
	}

	status := domain.EnrollmentRequestStatus{Conditions: []domain.Condition{}}
	if e.Status != nil {
		status = e.Status.Data
	}

	return &domain.EnrollmentRequest{
		ApiVersion: EnrollmentRequestAPIVersion(),
		Kind:       domain.EnrollmentRequestKind,
		Metadata: domain.ObjectMeta{
			Name:              lo.ToPtr(e.Name),
			CreationTimestamp: lo.ToPtr(e.CreatedAt.UTC()),
			Labels:            lo.ToPtr(util.EnsureMap(e.Resource.Labels)),
			Annotations:       lo.ToPtr(util.EnsureMap(e.Resource.Annotations)),
			ResourceVersion:   lo.Ternary(e.ResourceVersion != nil, lo.ToPtr(strconv.FormatInt(lo.FromPtr(e.ResourceVersion), 10)), nil),
		},
		Spec:   spec,
		Status: &status,
	}, nil
}

func EnrollmentRequestsToApiResource(ers []EnrollmentRequest, cont *string, numRemaining *int64) (domain.EnrollmentRequestList, error) {
	enrollmentRequestList := make([]domain.EnrollmentRequest, len(ers))
	for i, enrollmentRequest := range ers {
		apiResource, _ := enrollmentRequest.ToApiResource()
		enrollmentRequestList[i] = *apiResource
	}
	ret := domain.EnrollmentRequestList{
		ApiVersion: EnrollmentRequestAPIVersion(),
		Kind:       domain.EnrollmentRequestListKind,
		Items:      enrollmentRequestList,
		Metadata:   domain.ListMeta{},
	}
	if cont != nil {
		ret.Metadata.Continue = cont
		ret.Metadata.RemainingItemCount = numRemaining
	}
	return ret, nil
}

func (e *EnrollmentRequest) GetKind() string {
	return domain.EnrollmentRequestKind
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
	if e.Spec == nil && other.Spec == nil {
		return true
	}
	if (e.Spec == nil && other.Spec != nil) || (e.Spec != nil && other.Spec == nil) {
		return false
	}
	return reflect.DeepEqual(e.Spec.Data, other.Spec.Data)
}

func (e *EnrollmentRequest) GetStatusAsJson() ([]byte, error) {
	return e.Status.MarshalJSON()
}
