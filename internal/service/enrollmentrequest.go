package service

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/server"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/go-openapi/swag"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"k8s.io/apimachinery/pkg/labels"
)

const ClientCertExpiryDays = 365

type EnrollmentRequestStoreInterface interface {
	CreateEnrollmentRequest(ctx context.Context, orgId uuid.UUID, req *api.EnrollmentRequest) (*api.EnrollmentRequest, error)
	ListEnrollmentRequests(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.EnrollmentRequestList, error)
	GetEnrollmentRequest(ctx context.Context, orgId uuid.UUID, name string) (*api.EnrollmentRequest, error)
	CreateOrUpdateEnrollmentRequest(ctx context.Context, orgId uuid.UUID, enrollmentrequest *api.EnrollmentRequest) (*api.EnrollmentRequest, bool, error)
	UpdateEnrollmentRequestStatus(ctx context.Context, orgId uuid.UUID, enrollmentrequest *api.EnrollmentRequest) (*api.EnrollmentRequest, error)
	DeleteEnrollmentRequests(ctx context.Context, orgId uuid.UUID) error
	DeleteEnrollmentRequest(ctx context.Context, orgId uuid.UUID, name string) error
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
				LastTransitionTime: util.TimeStampStringPtr(),
			},
		},
	}
	return nil
}

func approveAndSignEnrollmentRequest(ca *crypto.CA, enrollmentRequest *api.EnrollmentRequest, approval *v1alpha1.EnrollmentRequestApproval) error {
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
				Reason:             util.StrToPtr("ManuallyApproved"),
				Message:            util.StrToPtr("Approved by " + *approval.ApprovedBy),
				LastTransitionTime: approval.ApprovedAt,
			},
		},
		Approval: approval,
	}
	return nil
}

func createDeviceFromEnrollmentRequest(ctx context.Context, deviceStore DeviceStoreInterface, orgId uuid.UUID, enrollmentRequest *api.EnrollmentRequest) error {
	apiResource := &api.Device{
		Metadata: api.ObjectMeta{
			Name: enrollmentRequest.Metadata.Name,
		},
	}
	if enrollmentRequest.Status.Approval != nil {
		apiResource.Metadata.Labels = enrollmentRequest.Status.Approval.Labels
		if apiResource.Metadata.Labels == nil {
			apiResource.Metadata.Labels = &map[string]string{}
		}
		(*apiResource.Metadata.Labels)["region"] = *enrollmentRequest.Status.Approval.Region
	}
	_, err := deviceStore.CreateDevice(ctx, orgId, apiResource)
	return err
}

// (POST /api/v1/enrollmentrequests)
func (h *ServiceHandler) CreateEnrollmentRequest(ctx context.Context, request server.CreateEnrollmentRequestRequestObject) (server.CreateEnrollmentRequestResponseObject, error) {
	orgId := NullOrgId

	if err := validateAndCompleteEnrollmentRequest(request.Body); err != nil {
		return nil, err
	}
	/*
		if err := autoApproveAndSignEnrollmentRequest(h.ca, request.Body); err != nil {
			return nil, err
		}
		if err := createDeviceFromEnrollmentRequest(h.deviceStore, orgId, request.Body); err != nil {
			return nil, err
		}
	*/

	result, err := h.enrollmentRequestStore.CreateEnrollmentRequest(ctx, orgId, request.Body)
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
	labelSelector := ""
	if request.Params.LabelSelector != nil {
		labelSelector = *request.Params.LabelSelector
	}

	labelMap, err := labels.ConvertSelectorToLabelsMap(labelSelector)
	if err != nil {
		return nil, err
	}

	cont, err := ParseContinueString(request.Params.Continue)
	if err != nil {
		return server.ListEnrollmentRequests400Response{}, fmt.Errorf("failed to parse continue parameter: %s", err)
	}

	listParams := ListParams{
		Labels:   labelMap,
		Limit:    int(swag.Int32Value(request.Params.Limit)),
		Continue: cont,
	}
	if listParams.Limit == 0 {
		listParams.Limit = MaxRecordsPerListRequest
	}
	if listParams.Limit > MaxRecordsPerListRequest {
		return server.ListEnrollmentRequests400Response{}, fmt.Errorf("limit cannot exceed %d", MaxRecordsPerListRequest)
	}

	result, err := h.enrollmentRequestStore.ListEnrollmentRequests(ctx, orgId, listParams)
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

	err := h.enrollmentRequestStore.DeleteEnrollmentRequests(ctx, orgId)
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

	result, err := h.enrollmentRequestStore.GetEnrollmentRequest(ctx, orgId, request.Name)
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

	result, created, err := h.enrollmentRequestStore.CreateOrUpdateEnrollmentRequest(ctx, orgId, request.Body)
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

	err := h.enrollmentRequestStore.DeleteEnrollmentRequest(ctx, orgId, request.Name)
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

	result, err := h.enrollmentRequestStore.GetEnrollmentRequest(ctx, orgId, request.Name)
	switch err {
	case nil:
		return server.ReadEnrollmentRequestStatus200JSONResponse(*result), nil
	case gorm.ErrRecordNotFound:
		return server.ReadEnrollmentRequestStatus404Response{}, nil
	default:
		return nil, err
	}
}

// (POST /api/v1/enrollmentrequests/{name}/approval)
func (h *ServiceHandler) CreateEnrollmentRequestApproval(ctx context.Context, request server.CreateEnrollmentRequestApprovalRequestObject) (server.CreateEnrollmentRequestApprovalResponseObject, error) {
	orgId := NullOrgId

	enrollmentReq, err := h.enrollmentRequestStore.GetEnrollmentRequest(ctx, orgId, request.Name)
	switch err {
	default:
		return nil, err
	case gorm.ErrRecordNotFound:
		return server.CreateEnrollmentRequestApproval404Response{}, nil
	case nil:
	}

	if request.Body.Approved {

		if request.Body.ApprovedAt != nil {
			return server.CreateEnrollmentRequestApproval422JSONResponse{
				Error: "ApprovedAt is not allowed to be set when approving enrollment requests",
			}, nil
		}

		request.Body.ApprovedAt = util.TimeStampStringPtr()

		// The same check should happen for ApprovedBy, but we don't have a way to identify
		// users yet, so we'll let the UI set it for now.
		if request.Body.ApprovedBy == nil {
			request.Body.ApprovedBy = util.StrToPtr("unknown")
		}

		if err := approveAndSignEnrollmentRequest(h.ca, enrollmentReq, request.Body); err != nil {
			return nil, err
		}

		if err := createDeviceFromEnrollmentRequest(ctx, h.deviceStore, orgId, enrollmentReq); err != nil {
			return nil, err
		}
	}

	_, err = h.enrollmentRequestStore.UpdateEnrollmentRequestStatus(ctx, orgId, enrollmentReq)
	switch err {
	case nil:
		return server.CreateEnrollmentRequestApproval200JSONResponse{}, nil
	case gorm.ErrRecordNotFound:
		return server.CreateEnrollmentRequestApproval404Response{}, nil
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

	result, err := h.enrollmentRequestStore.UpdateEnrollmentRequestStatus(ctx, orgId, request.Body)
	switch err {
	case nil:
		return server.ReplaceEnrollmentRequestStatus200JSONResponse(*result), nil
	case gorm.ErrRecordNotFound:
		return server.ReplaceEnrollmentRequestStatus404Response{}, nil
	default:
		return nil, err
	}
}
