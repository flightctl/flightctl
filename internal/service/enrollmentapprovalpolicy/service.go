package enrollmentapprovalpolicy

import (
	"context"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
)

type Service interface {
	CreateEnrollmentApprovalPolicy(ctx context.Context, orgId uuid.UUID, policy domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, domain.Status)
	ListEnrollmentApprovalPolicies(ctx context.Context, orgId uuid.UUID, params domain.ListEnrollmentApprovalPoliciesParams) (*domain.EnrollmentApprovalPolicyList, domain.Status)
	GetEnrollmentApprovalPolicy(ctx context.Context, orgId uuid.UUID, name string) (*domain.EnrollmentApprovalPolicy, domain.Status)
	ReplaceEnrollmentApprovalPolicy(ctx context.Context, orgId uuid.UUID, name string, policy domain.EnrollmentApprovalPolicy) (*domain.EnrollmentApprovalPolicy, domain.Status)
	PatchEnrollmentApprovalPolicy(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest) (*domain.EnrollmentApprovalPolicy, domain.Status)
	DeleteEnrollmentApprovalPolicy(ctx context.Context, orgId uuid.UUID, name string) domain.Status
}
