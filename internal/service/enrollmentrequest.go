package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/model"
	"github.com/flightctl/flightctl/pkg/server"
	"github.com/flightctl/flightctl/pkg/util"
	"github.com/google/uuid"
)

const ClientCertExpiryDays = 365

type EnrollmentRequestStoreInterface interface {
	CreateEnrollmentRequest(orgId uuid.UUID, req *model.EnrollmentRequest) (*model.EnrollmentRequest, error)
	ListEnrollmentRequests(orgId uuid.UUID) ([]model.EnrollmentRequest, error)
	GetEnrollmentRequest(orgId uuid.UUID, name string) (*model.EnrollmentRequest, error)
	UpdateEnrollmentRequest(orgId uuid.UUID, enrollmentrequest *model.EnrollmentRequest) (*model.EnrollmentRequest, error)
	UpdateEnrollmentRequestStatus(orgId uuid.UUID, enrollmentrequest *model.EnrollmentRequest) (*model.EnrollmentRequest, error)
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
			api.EnrollmentRequestCondition{
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
		Metadata: &api.ObjectMeta{
			Name: enrollmentRequest.Metadata.Name,
		},
	}
	newDevice := model.NewDeviceFromApiResource(apiResource)
	_, err := deviceStore.CreateDevice(orgId, newDevice)
	return err
}

// (POST /api/v1/enrollmentrequests)
func (h *ServiceHandler) CreateEnrollmentRequest(ctx context.Context, request server.CreateEnrollmentRequestRequestObject) (server.CreateEnrollmentRequestResponseObject, error) {
	orgId := NullOrgId
	if request.ContentType != "application/json" {
		return nil, fmt.Errorf("bad content type %s", request.ContentType)
	}

	var apiResource api.EnrollmentRequest
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&apiResource)
	if err != nil {
		return nil, err
	}

	if err := validateAndCompleteEnrollmentRequest(&apiResource); err != nil {
		return nil, err
	}

	if err := autoApproveAndSignEnrollmentRequest(h.ca, &apiResource); err != nil {
		return nil, err
	}
	if err := createDeviceFromEnrollmentRequest(h.deviceStore, orgId, &apiResource); err != nil {
		return nil, err
	}

	modelResource := model.NewEnrollmentRequestFromApiResource(&apiResource)
	result, err := h.enrollmentRequestStore.CreateEnrollmentRequest(orgId, modelResource)
	if err != nil {
		return nil, err
	}
	return server.CreateEnrollmentRequest201JSONResponse(result.ToApiResource()), nil
}

// (GET /api/v1/enrollmentrequests)
func (h *ServiceHandler) ListEnrollmentRequests(ctx context.Context, request server.ListEnrollmentRequestsRequestObject) (server.ListEnrollmentRequestsResponseObject, error) {
	orgId := NullOrgId
	enrollmentRequests, err := h.enrollmentRequestStore.ListEnrollmentRequests(orgId)
	if err != nil {
		return nil, err
	}
	return server.ListEnrollmentRequests200JSONResponse(model.EnrollmentRequestList(enrollmentRequests).ToApiResource()), nil
}

// (DELETE /api/v1/enrollmentrequests)
func (h *ServiceHandler) DeleteEnrollmentRequests(ctx context.Context, request server.DeleteEnrollmentRequestsRequestObject) (server.DeleteEnrollmentRequestsResponseObject, error) {
	orgId := NullOrgId
	err := h.enrollmentRequestStore.DeleteEnrollmentRequests(orgId)
	if err != nil {
		return nil, err
	}
	return server.DeleteEnrollmentRequests200JSONResponse{}, nil
}

// (GET /api/v1/enrollmentrequests/{name})
func (h *ServiceHandler) ReadEnrollmentRequest(ctx context.Context, request server.ReadEnrollmentRequestRequestObject) (server.ReadEnrollmentRequestResponseObject, error) {
	orgId := NullOrgId
	enrollmentRequest, err := h.enrollmentRequestStore.GetEnrollmentRequest(orgId, request.Name)
	if err != nil {
		return nil, err
	}
	return server.ReadEnrollmentRequest200JSONResponse(enrollmentRequest.ToApiResource()), nil
}

// (PUT /api/v1/enrollmentrequests/{name})
func (h *ServiceHandler) ReplaceEnrollmentRequest(ctx context.Context, request server.ReplaceEnrollmentRequestRequestObject) (server.ReplaceEnrollmentRequestResponseObject, error) {
	orgId := NullOrgId
	if request.ContentType != "application/json" {
		return nil, fmt.Errorf("bad content type %s", request.ContentType)
	}

	var apiResource api.EnrollmentRequest
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&apiResource)
	if err != nil {
		return nil, err
	}

	if err := validateAndCompleteEnrollmentRequest(&apiResource); err != nil {
		return nil, err
	}

	modelResource := model.NewEnrollmentRequestFromApiResource(&apiResource)
	enrollmentrequest, err := h.enrollmentRequestStore.UpdateEnrollmentRequest(orgId, modelResource)
	if err != nil {
		return nil, err
	}
	return server.ReplaceEnrollmentRequest200JSONResponse(enrollmentrequest.ToApiResource()), nil
}

// (DELETE /api/v1/enrollmentrequests/{name})
func (h *ServiceHandler) DeleteEnrollmentRequest(ctx context.Context, request server.DeleteEnrollmentRequestRequestObject) (server.DeleteEnrollmentRequestResponseObject, error) {
	orgId := NullOrgId
	if err := h.enrollmentRequestStore.DeleteEnrollmentRequest(orgId, request.Name); err != nil {
		return nil, err
	}
	return server.DeleteEnrollmentRequest200JSONResponse{}, nil
}

// (PUT /api/v1/enrollmentrequests/{name}/approval)
func (h *ServiceHandler) ReplaceEnrollmentRequestApproval(ctx context.Context, request server.ReplaceEnrollmentRequestApprovalRequestObject) (server.ReplaceEnrollmentRequestApprovalResponseObject, error) {
	orgId := NullOrgId
	if request.ContentType != "application/json" {
		return nil, fmt.Errorf("bad content type %s", request.ContentType)
	}

	var apiResource api.EnrollmentRequest
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&apiResource)
	if err != nil {
		return nil, err
	}

	if err := validateAndCompleteEnrollmentRequest(&apiResource); err != nil {
		return nil, err
	}

	modelResource := model.NewEnrollmentRequestFromApiResource(&apiResource)
	result, err := h.enrollmentRequestStore.UpdateEnrollmentRequestStatus(orgId, modelResource)
	if err != nil {
		return nil, err
	}
	return server.ReplaceEnrollmentRequestApproval200JSONResponse(result.ToApiResource()), nil
}

// (GET /api/v1/enrollmentrequests/{name}/status)
func (h *ServiceHandler) ReadEnrollmentRequestStatus(ctx context.Context, request server.ReadEnrollmentRequestStatusRequestObject) (server.ReadEnrollmentRequestStatusResponseObject, error) {
	orgId := NullOrgId
	enrollmentRequest, err := h.enrollmentRequestStore.GetEnrollmentRequest(orgId, request.Name)
	if err != nil {
		return nil, err
	}
	return server.ReadEnrollmentRequestStatus200JSONResponse(enrollmentRequest.ToApiResource()), nil
}

// (PUT /api/v1/enrollmentrequests/{name}/status)
func (h *ServiceHandler) ReplaceEnrollmentRequestStatus(ctx context.Context, request server.ReplaceEnrollmentRequestStatusRequestObject) (server.ReplaceEnrollmentRequestStatusResponseObject, error) {
	orgId := NullOrgId
	if request.ContentType != "application/json" {
		return nil, fmt.Errorf("bad content type %s", request.ContentType)
	}

	var apiResource api.EnrollmentRequest
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&apiResource)
	if err != nil {
		return nil, err
	}

	if err := validateAndCompleteEnrollmentRequest(&apiResource); err != nil {
		return nil, err
	}

	modelResource := model.NewEnrollmentRequestFromApiResource(&apiResource)
	result, err := h.enrollmentRequestStore.UpdateEnrollmentRequestStatus(orgId, modelResource)
	if err != nil {
		return nil, err
	}
	return server.ReplaceEnrollmentRequestStatus200JSONResponse(result.ToApiResource()), nil
}
