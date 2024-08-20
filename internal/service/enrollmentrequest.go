package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/go-openapi/swag"
	"github.com/google/uuid"
	"k8s.io/apimachinery/pkg/labels"
)

const ClientCertExpiryDays = 365

func approveAndSignEnrollmentRequest(ca *crypto.CA, enrollmentRequest *v1alpha1.EnrollmentRequest, approval *v1alpha1.EnrollmentRequestApproval) error {
	if enrollmentRequest == nil {
		return errors.New("approveAndSignEnrollmentRequest: enrollmentRequest is nil")
	}

	if enrollmentRequest.Metadata.Name == nil {
		return fmt.Errorf("approveAndSignEnrollmentRequest: enrollment request is missing metadata.name")
	}

	csr, err := crypto.ParseCSR([]byte(enrollmentRequest.Spec.Csr))
	if err != nil {
		return fmt.Errorf("approveAndSignEnrollmentRequest: error parsing CSR: %w", err)
	}

	csr.Subject.CommonName, err = crypto.CNFromDeviceFingerprint(*enrollmentRequest.Metadata.Name)
	if err != nil {
		return fmt.Errorf("approveAndSignEnrollmentRequest: error setting CN in CSR: %w", err)
	}

	if err := csr.CheckSignature(); err != nil {
		return err
	}

	expirySeconds := ClientCertExpiryDays * 24 * 60 * 60
	certData, err := ca.IssueRequestedClientCertificate(csr, expirySeconds)
	if err != nil {
		return err
	}
	enrollmentRequest.Status = &v1alpha1.EnrollmentRequestStatus{
		Certificate: util.StrToPtr(string(certData)),
		Conditions:  []v1alpha1.Condition{},
		Approval:    approval,
	}

	// union user-provided labels with agent-provided labels
	if enrollmentRequest.Spec.Labels != nil {
		for k, v := range *enrollmentRequest.Spec.Labels {
			// don't override user-provided labels
			if _, ok := (*enrollmentRequest.Status.Approval.Labels)[k]; !ok {
				(*enrollmentRequest.Status.Approval.Labels)[k] = v
			}
		}
	}

	condition := v1alpha1.Condition{
		Type:    v1alpha1.EnrollmentRequestApproved,
		Status:  v1alpha1.ConditionStatusTrue,
		Reason:  "ManuallyApproved",
		Message: "Approved by " + *approval.ApprovedBy,
	}
	v1alpha1.SetStatusCondition(&enrollmentRequest.Status.Conditions, condition)
	return nil
}

func (h *ServiceHandler) createDeviceFromEnrollmentRequest(ctx context.Context, orgId uuid.UUID, enrollmentRequest *v1alpha1.EnrollmentRequest) error {
	apiResource := &v1alpha1.Device{
		Metadata: v1alpha1.ObjectMeta{
			Name: enrollmentRequest.Metadata.Name,
		},
	}
	if errs := apiResource.Validate(); len(errs) > 0 {
		return fmt.Errorf("failed validating new device: %w", errors.Join(errs...))
	}
	if enrollmentRequest.Status.Approval != nil {
		apiResource.Metadata.Labels = enrollmentRequest.Status.Approval.Labels
	}
	_, err := h.store.Device().Create(ctx, orgId, apiResource, h.callbackManager.DeviceUpdatedCallback)
	return err
}

// (POST /api/v1/enrollmentrequests)
func (h *ServiceHandler) CreateEnrollmentRequest(ctx context.Context, request server.CreateEnrollmentRequestRequestObject) (server.CreateEnrollmentRequestResponseObject, error) {
	return common.CreateEnrollmentRequest(ctx, h.store, request)
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
		return server.ListEnrollmentRequests400JSONResponse{Message: err.Error()}, nil
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
	return common.ReadEnrollmentRequest(ctx, h.store, request)
}

// (PUT /api/v1/enrollmentrequests/{name})
func (h *ServiceHandler) ReplaceEnrollmentRequest(ctx context.Context, request server.ReplaceEnrollmentRequestRequestObject) (server.ReplaceEnrollmentRequestResponseObject, error) {
	orgId := store.NullOrgId

	if errs := request.Body.Validate(); len(errs) > 0 {
		return server.ReplaceEnrollmentRequest400JSONResponse{Message: errors.Join(errs...).Error()}, nil
	}
	if request.Name != *request.Body.Metadata.Name {
		return server.ReplaceEnrollmentRequest400JSONResponse{Message: "resource name specified in metadata does not match name in path"}, nil
	}

	if err := common.ValidateAndCompleteEnrollmentRequest(request.Body); err != nil {
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
	case flterrors.ErrResourceNameIsNil, flterrors.ErrResourceIsNil, flterrors.ErrIllegalResourceVersionFormat:
		return server.ReplaceEnrollmentRequest400JSONResponse{Message: err.Error()}, nil
	case flterrors.ErrResourceNotFound:
		return server.ReplaceEnrollmentRequest404JSONResponse{}, nil
	case flterrors.ErrNoRowsUpdated, flterrors.ErrResourceVersionConflict:
		return server.ReplaceEnrollmentRequest409JSONResponse{}, nil
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
func (h *ServiceHandler) ApproveEnrollmentRequest(ctx context.Context, request server.ApproveEnrollmentRequestRequestObject) (server.ApproveEnrollmentRequestResponseObject, error) {
	orgId := store.NullOrgId

	if errs := request.Body.Validate(); len(errs) > 0 {
		return server.ApproveEnrollmentRequest400JSONResponse{Message: errors.Join(errs...).Error()}, nil
	}

	enrollmentReq, err := h.store.EnrollmentRequest().Get(ctx, orgId, request.Name)
	switch err {
	default:
		return nil, err
	case flterrors.ErrResourceNotFound:
		return server.ApproveEnrollmentRequest404JSONResponse{}, nil
	case nil:
	}

	if request.Body.Approved {

		if request.Body.ApprovedAt != nil {
			return server.ApproveEnrollmentRequest422JSONResponse{Message: "ApprovedAt is not allowed to be set when approving enrollment requests"}, nil
		}

		request.Body.ApprovedAt = util.TimeToPtr(time.Now())

		// The same check should happen for ApprovedBy, but we don't have a way to identify
		// users yet, so we'll let the UI set it for now.
		if request.Body.ApprovedBy == nil {
			request.Body.ApprovedBy = util.StrToPtr("unknown")
		}

		if err := approveAndSignEnrollmentRequest(h.ca, enrollmentReq, request.Body); err != nil {
			return server.ApproveEnrollmentRequest422JSONResponse{Message: fmt.Sprintf("Error approving and signing enrollment request: %v", err.Error())}, nil
		}

		if err := h.createDeviceFromEnrollmentRequest(ctx, orgId, enrollmentReq); err != nil {
			return server.ApproveEnrollmentRequest422JSONResponse{Message: fmt.Sprintf("Error creating device from enrollment request: %v", err.Error())}, nil
		}
	}

	_, err = h.store.EnrollmentRequest().UpdateStatus(ctx, orgId, enrollmentReq)
	switch err {
	case nil:
		return server.ApproveEnrollmentRequest200JSONResponse{}, nil
	case flterrors.ErrResourceNotFound:
		return server.ApproveEnrollmentRequest404JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (PUT /api/v1/enrollmentrequests/{name}/status)
func (h *ServiceHandler) ReplaceEnrollmentRequestStatus(ctx context.Context, request server.ReplaceEnrollmentRequestStatusRequestObject) (server.ReplaceEnrollmentRequestStatusResponseObject, error) {
	orgId := store.NullOrgId

	if err := common.ValidateAndCompleteEnrollmentRequest(request.Body); err != nil {
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
