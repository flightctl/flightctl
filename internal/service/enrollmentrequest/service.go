package enrollmentrequest

import (
	"context"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
)

// Service is the focused EnrollmentRequest service interface, extracted from the monolithic
// internal/service.Service (internal/service/enrollmentrequest.go).
type Service interface {
	CreateEnrollmentRequest(ctx context.Context, orgId uuid.UUID, er domain.EnrollmentRequest) (*domain.EnrollmentRequest, domain.Status)
	ListEnrollmentRequests(ctx context.Context, orgId uuid.UUID, params domain.ListEnrollmentRequestsParams) (*domain.EnrollmentRequestList, domain.Status)
	GetEnrollmentRequest(ctx context.Context, orgId uuid.UUID, name string) (*domain.EnrollmentRequest, domain.Status)
	ReplaceEnrollmentRequest(ctx context.Context, orgId uuid.UUID, name string, er domain.EnrollmentRequest) (*domain.EnrollmentRequest, domain.Status)
	PatchEnrollmentRequest(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest) (*domain.EnrollmentRequest, domain.Status)
	DeleteEnrollmentRequest(ctx context.Context, orgId uuid.UUID, name string) domain.Status
	GetEnrollmentRequestStatus(ctx context.Context, orgId uuid.UUID, name string) (*domain.EnrollmentRequest, domain.Status)
	ApproveEnrollmentRequest(ctx context.Context, orgId uuid.UUID, name string, approval domain.EnrollmentRequestApproval) (*domain.EnrollmentRequestApprovalStatus, domain.Status)
	ReplaceEnrollmentRequestStatus(ctx context.Context, orgId uuid.UUID, name string, er domain.EnrollmentRequest) (*domain.EnrollmentRequest, domain.Status)
}
