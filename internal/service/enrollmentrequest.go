package service

import (
	"context"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/go-openapi/swag"
	"github.com/google/uuid"
	"k8s.io/apimachinery/pkg/labels"
)

const ClientCertExpiryDays = 365

func validateAndCompleteEnrollmentRequest(enrollmentRequest *api.EnrollmentRequest) error {
	if enrollmentRequest.Status == nil {
		enrollmentRequest.Status = &api.EnrollmentRequestStatus{
			Certificate: nil,
			Conditions:  &[]api.Condition{},
		}
	}
	return nil
}

func approveAndSignEnrollmentRequest(ca *crypto.CA, enrollmentRequest *api.EnrollmentRequest, approval *api.EnrollmentRequestApproval) error {
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
		Conditions:  &[]api.Condition{},
		Approval:    approval,
	}
	condition := api.Condition{
		Type:    api.EnrollmentRequestApproved,
		Status:  api.ConditionStatusTrue,
		Reason:  util.StrToPtr("ManuallyApproved"),
		Message: util.StrToPtr("Approved by " + *approval.ApprovedBy),
	}
	api.SetStatusCondition(enrollmentRequest.Status.Conditions, condition)
	return nil
}

func (h *ServiceHandler) createDeviceFromEnrollmentRequest(ctx context.Context, orgId uuid.UUID, enrollmentRequest *api.EnrollmentRequest) error {
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
	_, err := h.store.Device().Create(ctx, orgId, apiResource, h.taskManager.DeviceUpdatedCallback)
	return err
}

// (POST /api/v1/enrollmentrequests)
func (h *ServiceHandler) CreateEnrollmentRequest(ctx context.Context, request server.CreateEnrollmentRequestRequestObject) (server.CreateEnrollmentRequestResponseObject, error) {
	orgId := store.NullOrgId

	// verify if the enrollment request already exists, and return it with a 208 status code if it does
	if enrollmentReq, err := h.store.EnrollmentRequest().Get(ctx, orgId, *request.Body.Metadata.Name); err == nil {
		return server.CreateEnrollmentRequest208JSONResponse(*enrollmentReq), nil
	}

	// if the enrollment request does not exist, create it
	if err := validateAndCompleteEnrollmentRequest(request.Body); err != nil {
		return nil, err
	}

	result, err := h.store.EnrollmentRequest().Create(ctx, orgId, request.Body)
	switch err {
	case nil:
		return server.CreateEnrollmentRequest201JSONResponse(*result), nil
	case flterrors.ErrResourceIsNil:
		return server.CreateEnrollmentRequest400JSONResponse{Message: err.Error()}, nil
	default:
		return nil, err
	}
}

// (GET /api/v1/enrollmentrequests)
func (h *ServiceHandler) ListEnrollmentRequests(ctx context.Context, request server.ListEnrollmentRequestsRequestObject) (server.ListEnrollmentRequestsResponseObject, error) {
	orgId := store.NullOrgId
	labelSelector := ""
	if request.Params.LabelSelector != nil {
		labelSelector = *request.Params.LabelSelector
	}

	labelMap, err := labels.ConvertSelectorToLabelsMap(labelSelector)
	if err != nil {
		return nil, err
	}

	cont, err := store.ParseContinueString(request.Params.Continue)
	if err != nil {
		return server.ListEnrollmentRequests400JSONResponse{Message: fmt.Sprintf("failed to parse continue parameter: %v", err)}, nil
	}

	listParams := store.ListParams{
		Labels:   labelMap,
		Limit:    int(swag.Int32Value(request.Params.Limit)),
		Continue: cont,
	}
	if listParams.Limit == 0 {
		listParams.Limit = store.MaxRecordsPerListRequest
	}
	if listParams.Limit > store.MaxRecordsPerListRequest {
		return server.ListEnrollmentRequests400JSONResponse{Message: fmt.Sprintf("limit cannot exceed %d", store.MaxRecordsPerListRequest)}, nil
	}

	result, err := h.store.EnrollmentRequest().List(ctx, orgId, listParams)
	switch err {
	case nil:
		return server.ListEnrollmentRequests200JSONResponse(*result), nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/enrollmentrequests)
func (h *ServiceHandler) DeleteEnrollmentRequests(ctx context.Context, request server.DeleteEnrollmentRequestsRequestObject) (server.DeleteEnrollmentRequestsResponseObject, error) {
	orgId := store.NullOrgId

	err := h.store.EnrollmentRequest().DeleteAll(ctx, orgId)
	switch err {
	case nil:
		return server.DeleteEnrollmentRequests200JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (GET /api/v1/enrollmentrequests/{name})
func (h *ServiceHandler) ReadEnrollmentRequest(ctx context.Context, request server.ReadEnrollmentRequestRequestObject) (server.ReadEnrollmentRequestResponseObject, error) {
	orgId := store.NullOrgId

	result, err := h.store.EnrollmentRequest().Get(ctx, orgId, request.Name)
	switch err {
	case nil:
		return server.ReadEnrollmentRequest200JSONResponse(*result), nil
	case flterrors.ErrResourceNotFound:
		return server.ReadEnrollmentRequest404JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (PUT /api/v1/enrollmentrequests/{name})
func (h *ServiceHandler) ReplaceEnrollmentRequest(ctx context.Context, request server.ReplaceEnrollmentRequestRequestObject) (server.ReplaceEnrollmentRequestResponseObject, error) {
	orgId := store.NullOrgId
	if request.Body.Metadata.Name == nil {
		return server.ReplaceEnrollmentRequest400JSONResponse{Message: "metadata.name is not specified"}, nil
	}
	if request.Name != *request.Body.Metadata.Name {
		return server.ReplaceEnrollmentRequest400JSONResponse{Message: "resource name specified in metadata does not match name in path"}, nil
	}

	if err := validateAndCompleteEnrollmentRequest(request.Body); err != nil {
		return nil, err
	}

	result, created, err := h.store.EnrollmentRequest().CreateOrUpdate(ctx, orgId, request.Body)
	switch err {
	case nil:
		if created {
			return server.ReplaceEnrollmentRequest201JSONResponse(*result), nil
		} else {
			return server.ReplaceEnrollmentRequest200JSONResponse(*result), nil
		}
	case flterrors.ErrResourceIsNil:
		return server.ReplaceEnrollmentRequest400JSONResponse{Message: err.Error()}, nil
	case flterrors.ErrResourceNameIsNil:
		return server.ReplaceEnrollmentRequest400JSONResponse{Message: err.Error()}, nil
	case flterrors.ErrResourceNotFound:
		return server.ReplaceEnrollmentRequest404JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/enrollmentrequests/{name})
func (h *ServiceHandler) DeleteEnrollmentRequest(ctx context.Context, request server.DeleteEnrollmentRequestRequestObject) (server.DeleteEnrollmentRequestResponseObject, error) {
	orgId := store.NullOrgId

	err := h.store.EnrollmentRequest().Delete(ctx, orgId, request.Name)
	switch err {
	case nil:
		return server.DeleteEnrollmentRequest200JSONResponse{}, nil
	case flterrors.ErrResourceNotFound:
		return server.DeleteEnrollmentRequest404JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (GET /api/v1/enrollmentrequests/{name}/status)
func (h *ServiceHandler) ReadEnrollmentRequestStatus(ctx context.Context, request server.ReadEnrollmentRequestStatusRequestObject) (server.ReadEnrollmentRequestStatusResponseObject, error) {
	orgId := store.NullOrgId

	result, err := h.store.EnrollmentRequest().Get(ctx, orgId, request.Name)
	switch err {
	case nil:
		return server.ReadEnrollmentRequestStatus200JSONResponse(*result), nil
	case flterrors.ErrResourceNotFound:
		return server.ReadEnrollmentRequestStatus404JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (POST /api/v1/enrollmentrequests/{name}/approval)
func (h *ServiceHandler) CreateEnrollmentRequestApproval(ctx context.Context, request server.CreateEnrollmentRequestApprovalRequestObject) (server.CreateEnrollmentRequestApprovalResponseObject, error) {
	orgId := store.NullOrgId
	enrollmentReq, err := h.store.EnrollmentRequest().Get(ctx, orgId, request.Name)
	switch err {
	default:
		return nil, err
	case flterrors.ErrResourceNotFound:
		return server.CreateEnrollmentRequestApproval404JSONResponse{}, nil
	case nil:
	}

	if request.Body.Approved {

		if request.Body.ApprovedAt != nil {
			return server.CreateEnrollmentRequestApproval422JSONResponse{Message: "ApprovedAt is not allowed to be set when approving enrollment requests"}, nil
		}

		request.Body.ApprovedAt = util.TimeStampStringPtr()

		// The same check should happen for ApprovedBy, but we don't have a way to identify
		// users yet, so we'll let the UI set it for now.
		if request.Body.ApprovedBy == nil {
			request.Body.ApprovedBy = util.StrToPtr("unknown")
		}

		if err := approveAndSignEnrollmentRequest(h.ca, enrollmentReq, request.Body); err != nil {
			return server.CreateEnrollmentRequestApproval422JSONResponse{Message: fmt.Sprintf("Error approving and signing enrollment request: %v", err.Error())}, nil
		}

		if err := h.createDeviceFromEnrollmentRequest(ctx, orgId, enrollmentReq); err != nil {
			return server.CreateEnrollmentRequestApproval422JSONResponse{Message: fmt.Sprintf("Error creating device from enrollment request: %v", err.Error())}, nil
		}
	}

	_, err = h.store.EnrollmentRequest().UpdateStatus(ctx, orgId, enrollmentReq)
	switch err {
	case nil:
		return server.CreateEnrollmentRequestApproval200JSONResponse{}, nil
	case flterrors.ErrResourceNotFound:
		return server.CreateEnrollmentRequestApproval404JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (PUT /api/v1/enrollmentrequests/{name}/status)
func (h *ServiceHandler) ReplaceEnrollmentRequestStatus(ctx context.Context, request server.ReplaceEnrollmentRequestStatusRequestObject) (server.ReplaceEnrollmentRequestStatusResponseObject, error) {
	orgId := store.NullOrgId

	if err := validateAndCompleteEnrollmentRequest(request.Body); err != nil {
		return nil, err
	}

	result, err := h.store.EnrollmentRequest().UpdateStatus(ctx, orgId, request.Body)
	switch err {
	case nil:
		return server.ReplaceEnrollmentRequestStatus200JSONResponse(*result), nil
	case flterrors.ErrResourceNotFound:
		return server.ReplaceEnrollmentRequestStatus404JSONResponse{}, nil
	default:
		return nil, err
	}
}
