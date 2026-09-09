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

type EnrollmentHookPolicy struct {
	Resource

	Spec   *JSONField[domain.EnrollmentHookPolicySpec]   `gorm:"type:jsonb"`
	Status *JSONField[domain.EnrollmentHookPolicyStatus] `gorm:"type:jsonb"`
}

func (p EnrollmentHookPolicy) String() string {
	val, _ := json.Marshal(p)
	return string(val)
}

func NewEnrollmentHookPolicyFromApiResource(resource *domain.EnrollmentHookPolicy) (*EnrollmentHookPolicy, error) {
	if resource == nil || resource.Metadata.Name == nil {
		return &EnrollmentHookPolicy{}, nil
	}

	status := domain.EnrollmentHookPolicyStatus{Conditions: &[]domain.Condition{}}
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
	return &EnrollmentHookPolicy{
		Resource: Resource{
			Name:            *resource.Metadata.Name,
			Labels:          lo.FromPtrOr(resource.Metadata.Labels, make(map[string]string)),
			Annotations:     lo.FromPtr(resource.Metadata.Annotations),
			ResourceVersion: resourceVersion,
		},
		Spec:   MakeJSONField(resource.Spec),
		Status: MakeJSONField(status),
	}, nil
}

func EnrollmentHookPolicyAPIVersion() string {
	return fmt.Sprintf("%s/%s", domain.APIGroup, domain.EnrollmentHookPolicyAPIVersion)
}

func (p *EnrollmentHookPolicy) ToApiResource(opts ...APIResourceOption) (*domain.EnrollmentHookPolicy, error) {
	if p == nil {
		return &domain.EnrollmentHookPolicy{}, nil
	}

	var spec domain.EnrollmentHookPolicySpec
	if p.Spec != nil {
		spec = p.Spec.Data
	}

	status := domain.EnrollmentHookPolicyStatus{Conditions: &[]domain.Condition{}}
	if p.Status != nil {
		status = p.Status.Data
	}

	return &domain.EnrollmentHookPolicy{
		ApiVersion: EnrollmentHookPolicyAPIVersion(),
		Kind:       domain.EnrollmentHookPolicyKind,
		Metadata: domain.ObjectMeta{
			Name:              lo.ToPtr(p.Name),
			CreationTimestamp: lo.ToPtr(p.CreatedAt.UTC()),
			Labels:            lo.ToPtr(util.EnsureMap(p.Resource.Labels)),
			Annotations:       lo.ToPtr(util.EnsureMap(p.Resource.Annotations)),
			ResourceVersion:   lo.Ternary(p.ResourceVersion != nil, lo.ToPtr(strconv.FormatInt(lo.FromPtr(p.ResourceVersion), 10)), nil),
		},
		Spec:   spec,
		Status: &status,
	}, nil
}

func EnrollmentHookPoliciesToApiResource(policies []EnrollmentHookPolicy, cont *string, numRemaining *int64) (domain.EnrollmentHookPolicyList, error) {
	items := make([]domain.EnrollmentHookPolicy, len(policies))
	for i, p := range policies {
		item, err := p.ToApiResource()
		if err != nil {
			return domain.EnrollmentHookPolicyList{
				ApiVersion: EnrollmentHookPolicyAPIVersion(),
				Kind:       domain.EnrollmentHookPolicyListKind,
				Items:      []domain.EnrollmentHookPolicy{},
			}, err
		}
		items[i] = *item
	}
	ret := domain.EnrollmentHookPolicyList{
		ApiVersion: EnrollmentHookPolicyAPIVersion(),
		Kind:       domain.EnrollmentHookPolicyListKind,
		Items:      items,
		Metadata:   domain.ListMeta{},
	}
	if cont != nil {
		ret.Metadata.Continue = cont
		ret.Metadata.RemainingItemCount = numRemaining
	}
	return ret, nil
}

func (p *EnrollmentHookPolicy) GetKind() string {
	return domain.EnrollmentHookPolicyKind
}

func (p *EnrollmentHookPolicy) HasNilSpec() bool {
	return p.Spec == nil
}

func (p *EnrollmentHookPolicy) HasSameSpecAs(otherResource any) bool {
	other, ok := otherResource.(*EnrollmentHookPolicy)
	if !ok || other == nil {
		return false
	}
	if p.Spec == nil && other.Spec == nil {
		return true
	}
	if (p.Spec == nil) != (other.Spec == nil) {
		return false
	}
	return reflect.DeepEqual(p.Spec.Data, other.Spec.Data)
}

func (p *EnrollmentHookPolicy) GetStatusAsJson() ([]byte, error) {
	return p.Status.MarshalJSON()
}
