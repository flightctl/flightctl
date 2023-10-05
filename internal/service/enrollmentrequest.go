package service

import (
	"context"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/model"
	"github.com/flightctl/flightctl/pkg/server"
	"github.com/google/uuid"
)

type EnrollmentRequestStoreInterface interface {
	CreateEnrollmentRequest(orgId uuid.UUID, name string) (model.EnrollmentRequest, error)
	ListEnrollmentRequests(orgId uuid.UUID) ([]model.EnrollmentRequest, error)
	GetEnrollmentRequest(orgId uuid.UUID, name string) (model.EnrollmentRequest, error)
	WriteEnrollmentRequestSpec(orgId uuid.UUID, name string, spec api.EnrollmentRequestSpec) error
	WriteEnrollmentRequestStatus(orgId uuid.UUID, name string, status api.EnrollmentRequestStatus) error
	DeleteEnrollmentRequests(orgId uuid.UUID) error
	DeleteEnrollmentRequest(orgId uuid.UUID, name string) error
}

// (DELETE /api/v1/enrollmentrequests)
func (h *ServiceHandler) DeleteEnrollmentRequests(ctx context.Context, request server.DeleteEnrollmentRequestsRequestObject) (server.DeleteEnrollmentRequestsResponseObject, error) {
	return nil, nil
}

// (GET /api/v1/enrollmentrequests)
func (h *ServiceHandler) ListEnrollmentRequests(ctx context.Context, request server.ListEnrollmentRequestsRequestObject) (server.ListEnrollmentRequestsResponseObject, error) {
	return nil, nil
}

// (POST /api/v1/enrollmentrequests)
func (h *ServiceHandler) CreateEnrollmentRequest(ctx context.Context, request server.CreateEnrollmentRequestRequestObject) (server.CreateEnrollmentRequestResponseObject, error) {
	return nil, nil
}

// (GET /api/v1/enrollmentrequests/{name})
func (h *ServiceHandler) ReadEnrollmentRequest(ctx context.Context, request server.ReadEnrollmentRequestRequestObject) (server.ReadEnrollmentRequestResponseObject, error) {
	return nil, nil
}

// (PUT /api/v1/enrollmentrequests/{name})
func (h *ServiceHandler) ReplaceEnrollmentRequest(ctx context.Context, request server.ReplaceEnrollmentRequestRequestObject) (server.ReplaceEnrollmentRequestResponseObject, error) {
	return nil, nil
}

// (PUT /api/v1/enrollmentrequests/{name}/approve)
func (h *ServiceHandler) ApproveEnrollmentRequest(ctx context.Context, request server.ApproveEnrollmentRequestRequestObject) (server.ApproveEnrollmentRequestResponseObject, error) {
	return nil, nil
}

// (GET /api/v1/enrollmentrequests/{name}/status)
func (h *ServiceHandler) ReadEnrollmentRequestStatus(ctx context.Context, request server.ReadEnrollmentRequestStatusRequestObject) (server.ReadEnrollmentRequestStatusResponseObject, error) {
	return nil, nil
}

// (PUT /api/v1/enrollmentrequests/{name}/status)
func (h *ServiceHandler) ReplaceEnrollmentRequestStatus(ctx context.Context, request server.ReplaceEnrollmentRequestStatusRequestObject) (server.ReplaceEnrollmentRequestStatusResponseObject, error) {
	return nil, nil
}
