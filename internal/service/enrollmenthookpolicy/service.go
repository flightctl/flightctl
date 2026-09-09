package enrollmenthookpolicy

import (
	"context"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
)

type Service interface {
	CreateEnrollmentHookPolicy(ctx context.Context, orgId uuid.UUID, policy domain.EnrollmentHookPolicy) (*domain.EnrollmentHookPolicy, domain.Status)
	ListEnrollmentHookPolicies(ctx context.Context, orgId uuid.UUID, params domain.ListEnrollmentHookPoliciesParams) (*domain.EnrollmentHookPolicyList, domain.Status)
	GetEnrollmentHookPolicy(ctx context.Context, orgId uuid.UUID, name string) (*domain.EnrollmentHookPolicy, domain.Status)
	ReplaceEnrollmentHookPolicy(ctx context.Context, orgId uuid.UUID, name string, policy domain.EnrollmentHookPolicy) (*domain.EnrollmentHookPolicy, domain.Status)
	DeleteEnrollmentHookPolicy(ctx context.Context, orgId uuid.UUID, name string) domain.Status
	PatchEnrollmentHookPolicy(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest) (*domain.EnrollmentHookPolicy, domain.Status)
}
