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

type EnrollmentApprovalPolicy struct {
	Resource

	CertificateName string

	Spec   *JSONField[domain.EnrollmentApprovalPolicySpec]   `gorm:"type:jsonb"`
	Status *JSONField[domain.EnrollmentApprovalPolicyStatus] `gorm:"type:jsonb"`
}

func (policyRecord EnrollmentApprovalPolicy) String() string {
	val, _ := json.Marshal(policyRecord)
	return string(val)
}

func NewEnrollmentApprovalPolicyFromApiResource(resource *domain.EnrollmentApprovalPolicy) (*EnrollmentApprovalPolicy, error) {
	if resource == nil || resource.Metadata.Name == nil {
		return &EnrollmentApprovalPolicy{}, nil
	}

	status := domain.EnrollmentApprovalPolicyStatus{}
	if resource.Status != nil {
		status = *resource.Status
	}
	var resourceVersion *int64
	if resource.Metadata.ResourceVersion != nil {
		index, err := strconv.ParseInt(lo.FromPtr(resource.Metadata.ResourceVersion), 10, 64)
		if err != nil {
			return nil, flterrors.ErrIllegalResourceVersionFormat
		}
		resourceVersion = &index
	}
	return &EnrollmentApprovalPolicy{
		Resource: Resource{
			Name:            *resource.Metadata.Name,
			Labels:          lo.FromPtrOr(resource.Metadata.Labels, make(map[string]string)),
			Annotations:     lo.FromPtr(resource.Metadata.Annotations),
			ResourceVersion: resourceVersion,
		},
		CertificateName: resource.Spec.CertificateName,
		Spec:            MakeJSONField(resource.Spec),
		Status:          MakeJSONField(status),
	}, nil
}

func EnrollmentApprovalPolicyAPIVersion() string {
	return fmt.Sprintf("%s/%s", domain.APIGroup, domain.EnrollmentApprovalPolicyAPIVersion)
}

func (policyRecord *EnrollmentApprovalPolicy) ToApiResource(opts ...APIResourceOption) (*domain.EnrollmentApprovalPolicy, error) {
	if policyRecord == nil {
		return &domain.EnrollmentApprovalPolicy{}, nil
	}

	var spec domain.EnrollmentApprovalPolicySpec
	if policyRecord.Spec != nil {
		spec = policyRecord.Spec.Data
	}

	status := domain.EnrollmentApprovalPolicyStatus{}
	if policyRecord.Status != nil {
		status = policyRecord.Status.Data
	}

	return &domain.EnrollmentApprovalPolicy{
		ApiVersion: EnrollmentApprovalPolicyAPIVersion(),
		Kind:       domain.EnrollmentApprovalPolicyKind,
		Metadata: domain.ObjectMeta{
			Name:              lo.ToPtr(policyRecord.Name),
			CreationTimestamp: lo.ToPtr(policyRecord.CreatedAt.UTC()),
			Labels:            lo.ToPtr(util.EnsureMap(policyRecord.Resource.Labels)),
			Annotations:       lo.ToPtr(util.EnsureMap(policyRecord.Resource.Annotations)),
			ResourceVersion:   lo.Ternary(policyRecord.ResourceVersion != nil, lo.ToPtr(strconv.FormatInt(lo.FromPtr(policyRecord.ResourceVersion), 10)), nil),
			Generation:        clonePtr(policyRecord.Generation),
		},
		Spec:   spec,
		Status: &status,
	}, nil
}

func EnrollmentApprovalPoliciesToApiResource(policies []EnrollmentApprovalPolicy, cont *string, numRemaining *int64) (domain.EnrollmentApprovalPolicyList, error) {
	items := make([]domain.EnrollmentApprovalPolicy, len(policies))
	for index, policyRecord := range policies {
		item, err := policyRecord.ToApiResource()
		if err != nil {
			return domain.EnrollmentApprovalPolicyList{
				ApiVersion: EnrollmentApprovalPolicyAPIVersion(),
				Kind:       domain.EnrollmentApprovalPolicyListKind,
				Items:      []domain.EnrollmentApprovalPolicy{},
			}, err
		}
		items[index] = *item
	}
	ret := domain.EnrollmentApprovalPolicyList{
		ApiVersion: EnrollmentApprovalPolicyAPIVersion(),
		Kind:       domain.EnrollmentApprovalPolicyListKind,
		Items:      items,
		Metadata:   domain.ListMeta{},
	}
	if cont != nil {
		ret.Metadata.Continue = cont
		ret.Metadata.RemainingItemCount = numRemaining
	}
	return ret, nil
}

func (policyRecord *EnrollmentApprovalPolicy) GetKind() string {
	return domain.EnrollmentApprovalPolicyKind
}

func (policyRecord *EnrollmentApprovalPolicy) HasNilSpec() bool {
	return policyRecord.Spec == nil
}

func (policyRecord *EnrollmentApprovalPolicy) HasSameSpecAs(otherResource any) bool {
	other, ok := otherResource.(*EnrollmentApprovalPolicy)
	if !ok || other == nil {
		return false
	}
	if policyRecord.Spec == nil && other.Spec == nil {
		return true
	}
	if (policyRecord.Spec == nil) != (other.Spec == nil) {
		return false
	}
	return reflect.DeepEqual(policyRecord.Spec.Data, other.Spec.Data)
}

func (policyRecord *EnrollmentApprovalPolicy) GetStatusAsJson() ([]byte, error) {
	if policyRecord.Status == nil {
		return []byte("null"), nil
	}
	return policyRecord.Status.MarshalJSON()
}
