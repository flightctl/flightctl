package v1beta1

import (
	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
)

type EnrollmentHookPolicyConverter interface {
	ToDomain(apiv1beta1.EnrollmentHookPolicy) domain.EnrollmentHookPolicy
	FromDomain(*domain.EnrollmentHookPolicy) *apiv1beta1.EnrollmentHookPolicy
	ListFromDomain(*domain.EnrollmentHookPolicyList) *apiv1beta1.EnrollmentHookPolicyList
	ListParamsToDomain(apiv1beta1.ListEnrollmentHookPoliciesParams) domain.ListEnrollmentHookPoliciesParams
}

type enrollmentHookPolicyConverter struct{}

func NewEnrollmentHookPolicyConverter() EnrollmentHookPolicyConverter {
	return &enrollmentHookPolicyConverter{}
}

func (c *enrollmentHookPolicyConverter) ToDomain(p apiv1beta1.EnrollmentHookPolicy) domain.EnrollmentHookPolicy {
	return p
}

func (c *enrollmentHookPolicyConverter) FromDomain(p *domain.EnrollmentHookPolicy) *apiv1beta1.EnrollmentHookPolicy {
	return p
}

func (c *enrollmentHookPolicyConverter) ListFromDomain(l *domain.EnrollmentHookPolicyList) *apiv1beta1.EnrollmentHookPolicyList {
	return l
}

func (c *enrollmentHookPolicyConverter) ListParamsToDomain(p apiv1beta1.ListEnrollmentHookPoliciesParams) domain.ListEnrollmentHookPoliciesParams {
	return p
}
