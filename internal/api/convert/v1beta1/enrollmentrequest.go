package v1beta1

import (
	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
)

// EnrollmentRequestConverter converts between v1beta1 API types and domain types for EnrollmentRequest resources.
type EnrollmentRequestConverter interface {
	ToDomain(apiv1beta1.EnrollmentRequest) domain.EnrollmentRequest
	FromDomain(*domain.EnrollmentRequest) *apiv1beta1.EnrollmentRequest
	ListFromDomain(*domain.EnrollmentRequestList) *apiv1beta1.EnrollmentRequestList

	// Approval types
	ApprovalToDomain(apiv1beta1.EnrollmentRequestApproval) domain.EnrollmentRequestApproval
	ApprovalStatusFromDomain(*domain.EnrollmentRequestApprovalStatus) *apiv1beta1.EnrollmentRequestApprovalStatus

	// Config types
	ConfigFromDomain(*domain.EnrollmentConfig) *apiv1beta1.EnrollmentConfig

	// Params conversions
	ListParamsToDomain(apiv1beta1.ListEnrollmentRequestsParams) domain.ListEnrollmentRequestsParams
	GetConfigParamsToDomain(apiv1beta1.GetEnrollmentConfigParams) domain.GetEnrollmentConfigParams
}

type enrollmentRequestConverter struct{}

// NewEnrollmentRequestConverter creates a new EnrollmentRequestConverter.
func NewEnrollmentRequestConverter() EnrollmentRequestConverter {
	return &enrollmentRequestConverter{}
}

func (c *enrollmentRequestConverter) ToDomain(er apiv1beta1.EnrollmentRequest) domain.EnrollmentRequest {
	return er
}

func (c *enrollmentRequestConverter) FromDomain(er *domain.EnrollmentRequest) *apiv1beta1.EnrollmentRequest {
	return er
}

func (c *enrollmentRequestConverter) ListFromDomain(l *domain.EnrollmentRequestList) *apiv1beta1.EnrollmentRequestList {
	return l
}

func (c *enrollmentRequestConverter) ApprovalToDomain(a apiv1beta1.EnrollmentRequestApproval) domain.EnrollmentRequestApproval {
	return a
}

func (c *enrollmentRequestConverter) ApprovalStatusFromDomain(s *domain.EnrollmentRequestApprovalStatus) *apiv1beta1.EnrollmentRequestApprovalStatus {
	return s
}

func (c *enrollmentRequestConverter) ConfigFromDomain(cfg *domain.EnrollmentConfig) *apiv1beta1.EnrollmentConfig {
	return cfg
}

func (c *enrollmentRequestConverter) ListParamsToDomain(p apiv1beta1.ListEnrollmentRequestsParams) domain.ListEnrollmentRequestsParams {
	return p
}

func (c *enrollmentRequestConverter) GetConfigParamsToDomain(p apiv1beta1.GetEnrollmentConfigParams) domain.GetEnrollmentConfigParams {
	return p
}
