package v1beta1

import (
	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
)

type EnrollmentApprovalPolicyConverter interface {
	ToDomain(apiv1beta1.EnrollmentApprovalPolicy) domain.EnrollmentApprovalPolicy
	FromDomain(*domain.EnrollmentApprovalPolicy) *apiv1beta1.EnrollmentApprovalPolicy
	ListFromDomain(*domain.EnrollmentApprovalPolicyList) *apiv1beta1.EnrollmentApprovalPolicyList
	ListParamsToDomain(apiv1beta1.ListEnrollmentApprovalPoliciesParams) domain.ListEnrollmentApprovalPoliciesParams
	StatusFromDomain(*domain.EnrollmentApprovalPolicyStatus) *apiv1beta1.EnrollmentApprovalPolicyStatus
}

type enrollmentApprovalPolicyConverter struct{}

func NewEnrollmentApprovalPolicyConverter() EnrollmentApprovalPolicyConverter {
	return &enrollmentApprovalPolicyConverter{}
}
func (*enrollmentApprovalPolicyConverter) ToDomain(policy apiv1beta1.EnrollmentApprovalPolicy) domain.EnrollmentApprovalPolicy {
	return policy
}
func (*enrollmentApprovalPolicyConverter) FromDomain(policy *domain.EnrollmentApprovalPolicy) *apiv1beta1.EnrollmentApprovalPolicy {
	return policy
}
func (*enrollmentApprovalPolicyConverter) ListFromDomain(list *domain.EnrollmentApprovalPolicyList) *apiv1beta1.EnrollmentApprovalPolicyList {
	return list
}
func (*enrollmentApprovalPolicyConverter) ListParamsToDomain(params apiv1beta1.ListEnrollmentApprovalPoliciesParams) domain.ListEnrollmentApprovalPoliciesParams {
	return params
}
func (*enrollmentApprovalPolicyConverter) StatusFromDomain(status *domain.EnrollmentApprovalPolicyStatus) *apiv1beta1.EnrollmentApprovalPolicyStatus {
	return status
}
