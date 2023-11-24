package service

import (
	"context"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/server"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const ClientCertExpiryDays = 365

type EnrollmentRequestStoreInterface interface {
	CreateEnrollmentRequest(orgId uuid.UUID, req *api.EnrollmentRequest) (*api.EnrollmentRequest, error)
	ListEnrollmentRequests(orgId uuid.UUID) (*api.EnrollmentRequestList, error)
	GetEnrollmentRequest(orgId uuid.UUID, name string) (*api.EnrollmentRequest, error)
	CreateOrUpdateEnrollmentRequest(orgId uuid.UUID, enrollmentrequest *api.EnrollmentRequest) (*api.EnrollmentRequest, bool, error)
	UpdateEnrollmentRequestStatus(orgId uuid.UUID, enrollmentrequest *api.EnrollmentRequest) (*api.EnrollmentRequest, error)
	DeleteEnrollmentRequests(orgId uuid.UUID) error
	DeleteEnrollmentRequest(orgId uuid.UUID, name string) error
}

func validateAndCompleteEnrollmentRequest(enrollmentRequest *api.EnrollmentRequest) error {
	if enrollmentRequest.Status == nil {
		enrollmentRequest.Status = &api.EnrollmentRequestStatus{
			Certificate: nil,
			Conditions:  &[]api.EnrollmentRequestCondition{},
		}
	}
	return nil
}

func autoApproveAndSignEnrollmentRequest(ca *crypto.CA, enrollmentRequest *api.EnrollmentRequest) error {
	csrPEM := enrollmentRequest.Spec.Csr
	csr, err := crypto.ParseCSR([]byte(csrPEM))
	if err != nil {
		return err
	}
	if err := csr.CheckSignature(); err != nil {
		return err
	}
	certData, err := ca.IssueRequestedClientCertificate(csr, ClientCertExpiryDays)
	if err != nil {
		return err
	}
	enrollmentRequest.Status = &api.EnrollmentRequestStatus{
		Certificate: util.StrToPtr(string(certData)),
		Conditions: &[]api.EnrollmentRequestCondition{
			{
				Type:               "Approved",
				Status:             "True",
				Reason:             util.StrToPtr("AutoApproved"),
				Message:            util.StrToPtr("Approved by auto approver"),
				LastTransitionTime: util.StrToPtr(time.Now().Format(time.RFC3339)),
			},
		},
	}
	return nil
}

func createDeviceFromEnrollmentRequest(deviceStore DeviceStoreInterface, orgId uuid.UUID, enrollmentRequest *api.EnrollmentRequest) error {
	apiResource := &api.Device{
		Metadata: api.ObjectMeta{
			Name: enrollmentRequest.Metadata.Name,
		},
	}
	_, err := deviceStore.CreateDevice(orgId, apiResource)
	return err
}

// (POST /api/v1/enrollmentrequests)
func (h *ServiceHandler) CreateEnrollmentRequest(ctx context.Context, request server.CreateEnrollmentRequestRequestObject) (server.CreateEnrollmentRequestResponseObject, error) {
	orgId := NullOrgId

	if err := validateAndCompleteEnrollmentRequest(request.Body); err != nil {
		return nil, err
	}
	if err := autoApproveAndSignEnrollmentRequest(h.ca, request.Body); err != nil {
		return nil, err
	}
	if err := createDeviceFromEnrollmentRequest(h.deviceStore, orgId, request.Body); err != nil {
		return nil, err
	}

	result, err := h.enrollmentRequestStore.CreateEnrollmentRequest(orgId, request.Body)
	switch err {
	case nil:
		return server.CreateEnrollmentRequest201JSONResponse(*result), nil
	default:
		return nil, err
	}
}

// (GET /api/v1/enrollmentrequests)
func (h *ServiceHandler) ListEnrollmentRequests(ctx context.Context, request server.ListEnrollmentRequestsRequestObject) (server.ListEnrollmentRequestsResponseObject, error) {
	orgId := NullOrgId

	result, err := h.enrollmentRequestStore.ListEnrollmentRequests(orgId)
	switch err {
	case nil:
		return server.ListEnrollmentRequests200JSONResponse(*result), nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/enrollmentrequests)
func (h *ServiceHandler) DeleteEnrollmentRequests(ctx context.Context, request server.DeleteEnrollmentRequestsRequestObject) (server.DeleteEnrollmentRequestsResponseObject, error) {
	orgId := NullOrgId

	err := h.enrollmentRequestStore.DeleteEnrollmentRequests(orgId)
	switch err {
	case nil:
		return server.DeleteEnrollmentRequests200JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (GET /api/v1/enrollmentrequests/{name})
func (h *ServiceHandler) ReadEnrollmentRequest(ctx context.Context, request server.ReadEnrollmentRequestRequestObject) (server.ReadEnrollmentRequestResponseObject, error) {
	orgId := NullOrgId

	result, err := h.enrollmentRequestStore.GetEnrollmentRequest(orgId, request.Name)
	switch err {
	case nil:
		return server.ReadEnrollmentRequest200JSONResponse(*result), nil
	case gorm.ErrRecordNotFound:
		return server.ReadEnrollmentRequest404Response{}, nil
	default:
		return nil, err
	}
}

// (PUT /api/v1/enrollmentrequests/{name})
func (h *ServiceHandler) ReplaceEnrollmentRequest(ctx context.Context, request server.ReplaceEnrollmentRequestRequestObject) (server.ReplaceEnrollmentRequestResponseObject, error) {
	orgId := NullOrgId

	if err := validateAndCompleteEnrollmentRequest(request.Body); err != nil {
		return nil, err
	}

	result, created, err := h.enrollmentRequestStore.CreateOrUpdateEnrollmentRequest(orgId, request.Body)
	switch err {
	case nil:
		if created {
			return server.ReplaceEnrollmentRequest201JSONResponse(*result), nil
		} else {
			return server.ReplaceEnrollmentRequest200JSONResponse(*result), nil
		}
	case gorm.ErrRecordNotFound:
		return server.ReplaceEnrollmentRequest404Response{}, nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/enrollmentrequests/{name})
func (h *ServiceHandler) DeleteEnrollmentRequest(ctx context.Context, request server.DeleteEnrollmentRequestRequestObject) (server.DeleteEnrollmentRequestResponseObject, error) {
	orgId := NullOrgId

	err := h.enrollmentRequestStore.DeleteEnrollmentRequest(orgId, request.Name)
	switch err {
	case nil:
		return server.DeleteEnrollmentRequest200JSONResponse{}, nil
	case gorm.ErrRecordNotFound:
		return server.DeleteEnrollmentRequest404Response{}, nil
	default:
		return nil, err
	}
}

// (GET /api/v1/enrollmentrequests/{name}/status)
func (h *ServiceHandler) ReadEnrollmentRequestStatus(ctx context.Context, request server.ReadEnrollmentRequestStatusRequestObject) (server.ReadEnrollmentRequestStatusResponseObject, error) {
	orgId := NullOrgId

	result, err := h.enrollmentRequestStore.GetEnrollmentRequest(orgId, request.Name)
	switch err {
	case nil:
		return server.ReadEnrollmentRequestStatus200JSONResponse(*result), nil
	case gorm.ErrRecordNotFound:
		return server.ReadEnrollmentRequestStatus404Response{}, nil
	default:
		return nil, err
	}
}

// (PUT /api/v1/enrollmentrequests/{name}/approval)
func (h *ServiceHandler) ReplaceEnrollmentRequestApproval(ctx context.Context, request server.ReplaceEnrollmentRequestApprovalRequestObject) (server.ReplaceEnrollmentRequestApprovalResponseObject, error) {
	orgId := NullOrgId

	if err := validateAndCompleteEnrollmentRequest(request.Body); err != nil {
		return nil, err
	}

	result, err := h.enrollmentRequestStore.UpdateEnrollmentRequestStatus(orgId, request.Body)
	switch err {
	case nil:
		return server.ReplaceEnrollmentRequestApproval200JSONResponse(*result), nil
	case gorm.ErrRecordNotFound:
		return server.ReplaceEnrollmentRequestApproval404Response{}, nil
	default:
		return nil, err
	}
}

// (PUT /api/v1/enrollmentrequests/{name}/status)
func (h *ServiceHandler) ReplaceEnrollmentRequestStatus(ctx context.Context, request server.ReplaceEnrollmentRequestStatusRequestObject) (server.ReplaceEnrollmentRequestStatusResponseObject, error) {
	orgId := NullOrgId

	if err := validateAndCompleteEnrollmentRequest(request.Body); err != nil {
		return nil, err
	}

	result, err := h.enrollmentRequestStore.UpdateEnrollmentRequestStatus(orgId, request.Body)
	switch err {
	case nil:
		return server.ReplaceEnrollmentRequestStatus200JSONResponse(*result), nil
	case gorm.ErrRecordNotFound:
		return server.ReplaceEnrollmentRequestStatus404Response{}, nil
	default:
		return nil, err
	}
}
