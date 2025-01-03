package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/auth"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/util"
	k8sselector "github.com/flightctl/flightctl/pkg/k8s/selector"
	"github.com/flightctl/flightctl/pkg/k8s/selector/fields"
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
		return fmt.Errorf("failed to verify signature of CSR: %w", err)
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
	status := v1alpha1.NewDeviceStatus()
	status.Lifecycle = v1alpha1.DeviceLifecycleStatus{Status: "Enrolled"}
	apiResource := &v1alpha1.Device{
		Metadata: v1alpha1.ObjectMeta{
			Name: enrollmentRequest.Metadata.Name,
		},
		Status: &status,
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
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "enrollmentrequests", "create")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.CreateEnrollmentRequest503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.CreateEnrollmentRequest403JSONResponse{Message: Forbidden}, nil
	}
	return common.CreateEnrollmentRequest(ctx, h.store, request)
}

// (GET /api/v1/enrollmentrequests)
func (h *ServiceHandler) ListEnrollmentRequests(ctx context.Context, request server.ListEnrollmentRequestsRequestObject) (server.ListEnrollmentRequestsResponseObject, error) {
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "enrollmentrequests", "list")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.ListEnrollmentRequests503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.ListEnrollmentRequests403JSONResponse{Message: Forbidden}, nil
	}
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

	var fieldSelector k8sselector.Selector
	if request.Params.FieldSelector != nil {
		if fieldSelector, err = fields.ParseSelector(*request.Params.FieldSelector); err != nil {
			return server.ListEnrollmentRequests400JSONResponse{Message: fmt.Sprintf("failed to parse field selector: %v", err)}, nil
		}
	}

	listParams := store.ListParams{
		Labels:        labelMap,
		Limit:         int(swag.Int32Value(request.Params.Limit)),
		Continue:      cont,
		FieldSelector: fieldSelector,
	}
	if listParams.Limit == 0 {
		listParams.Limit = store.MaxRecordsPerListRequest
	}
	if listParams.Limit > store.MaxRecordsPerListRequest {
		return server.ListEnrollmentRequests400JSONResponse{Message: fmt.Sprintf("limit cannot exceed %d", store.MaxRecordsPerListRequest)}, nil
	}

	result, err := h.store.EnrollmentRequest().List(ctx, orgId, listParams)
	if err == nil {
		return server.ListEnrollmentRequests200JSONResponse(*result), nil
	}

	var se *selector.SelectorError

	switch {
	case selector.AsSelectorError(err, &se):
		return server.ListEnrollmentRequests400JSONResponse{Message: se.Error()}, nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/enrollmentrequests)
func (h *ServiceHandler) DeleteEnrollmentRequests(ctx context.Context, request server.DeleteEnrollmentRequestsRequestObject) (server.DeleteEnrollmentRequestsResponseObject, error) {
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "enrollmentrequests", "deletecollection")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.DeleteEnrollmentRequests503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.DeleteEnrollmentRequests403JSONResponse{Message: Forbidden}, nil
	}
	orgId := store.NullOrgId

	err = h.store.EnrollmentRequest().DeleteAll(ctx, orgId)
	switch err {
	case nil:
		return server.DeleteEnrollmentRequests200JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (GET /api/v1/enrollmentrequests/{name})
func (h *ServiceHandler) ReadEnrollmentRequest(ctx context.Context, request server.ReadEnrollmentRequestRequestObject) (server.ReadEnrollmentRequestResponseObject, error) {
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "enrollmentrequests", "get")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.ReadEnrollmentRequest503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.ReadEnrollmentRequest403JSONResponse{Message: Forbidden}, nil
	}
	return common.ReadEnrollmentRequest(ctx, h.store, request)
}

// (PUT /api/v1/enrollmentrequests/{name})
func (h *ServiceHandler) ReplaceEnrollmentRequest(ctx context.Context, request server.ReplaceEnrollmentRequestRequestObject) (server.ReplaceEnrollmentRequestResponseObject, error) {
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "enrollmentrequests", "update")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.ReplaceEnrollmentRequest503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.ReplaceEnrollmentRequest403JSONResponse{Message: Forbidden}, nil
	}
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

// (PATCH /api/v1/enrollmentrequests/{name})
// Only metadata.labels and spec can be patched. If we try to patch other fields, HTTP 400 Bad Request is returned.
func (h *ServiceHandler) PatchEnrollmentRequest(ctx context.Context, request server.PatchEnrollmentRequestRequestObject) (server.PatchEnrollmentRequestResponseObject, error) {
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "enrollmentrequests", "patch")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.PatchEnrollmentRequest503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.PatchEnrollmentRequest403JSONResponse{Message: Forbidden}, nil
	}
	orgId := store.NullOrgId

	currentObj, err := h.store.EnrollmentRequest().Get(ctx, orgId, request.Name)
	if err != nil {
		switch err {
		case flterrors.ErrResourceIsNil, flterrors.ErrResourceNameIsNil:
			return server.PatchEnrollmentRequest400JSONResponse{Message: err.Error()}, nil
		case flterrors.ErrResourceNotFound:
			return server.PatchEnrollmentRequest404JSONResponse{}, nil
		default:
			return nil, err
		}
	}

	newObj := &v1alpha1.EnrollmentRequest{}
	err = ApplyJSONPatch(ctx, currentObj, newObj, *request.Body, "/api/v1/enrollmentrequests/"+request.Name)
	if err != nil {
		return server.PatchEnrollmentRequest400JSONResponse{Message: err.Error()}, nil
	}

	if errs := newObj.Validate(); len(errs) > 0 {
		return server.PatchEnrollmentRequest400JSONResponse{Message: errors.Join(errs...).Error()}, nil
	}
	if newObj.Metadata.Name == nil || *currentObj.Metadata.Name != *newObj.Metadata.Name {
		return server.PatchEnrollmentRequest400JSONResponse{Message: "metadata.name is immutable"}, nil
	}
	if currentObj.ApiVersion != newObj.ApiVersion {
		return server.PatchEnrollmentRequest400JSONResponse{Message: "apiVersion is immutable"}, nil
	}
	if currentObj.Kind != newObj.Kind {
		return server.PatchEnrollmentRequest400JSONResponse{Message: "kind is immutable"}, nil
	}
	if !reflect.DeepEqual(currentObj.Status, newObj.Status) {
		return server.PatchEnrollmentRequest400JSONResponse{Message: "status is immutable"}, nil
	}

	common.NilOutManagedObjectMetaProperties(&newObj.Metadata)
	newObj.Metadata.ResourceVersion = nil

	result, err := h.store.EnrollmentRequest().Update(ctx, orgId, newObj)
	switch err {
	case nil:
		return server.PatchEnrollmentRequest200JSONResponse(*result), nil
	case flterrors.ErrResourceIsNil, flterrors.ErrResourceNameIsNil, flterrors.ErrIllegalResourceVersionFormat:
		return server.PatchEnrollmentRequest400JSONResponse{Message: err.Error()}, nil
	case flterrors.ErrResourceNotFound:
		return server.PatchEnrollmentRequest404JSONResponse{}, nil
	case flterrors.ErrNoRowsUpdated, flterrors.ErrResourceVersionConflict, flterrors.ErrUpdatingResourceWithOwnerNotAllowed:
		return server.PatchEnrollmentRequest409JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/enrollmentrequests/{name})
func (h *ServiceHandler) DeleteEnrollmentRequest(ctx context.Context, request server.DeleteEnrollmentRequestRequestObject) (server.DeleteEnrollmentRequestResponseObject, error) {
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "enrollmentrequests", "delete")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.DeleteEnrollmentRequest503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.DeleteEnrollmentRequest403JSONResponse{Message: Forbidden}, nil
	}
	orgId := store.NullOrgId

	err = h.store.EnrollmentRequest().Delete(ctx, orgId, request.Name)
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
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "enrollmentrequests/status", "get")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.ReadEnrollmentRequestStatus503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.ReadEnrollmentRequestStatus403JSONResponse{Message: Forbidden}, nil
	}
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
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "enrollmentrequests/approval", "post")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.ApproveEnrollmentRequest503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.ApproveEnrollmentRequest403JSONResponse{Message: Forbidden}, nil
	}
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

	// if the enrollment request was already approved we should not try to approve it one more time
	if request.Body.Approved {
		if v1alpha1.IsStatusConditionTrue(enrollmentReq.Status.Conditions, v1alpha1.EnrollmentRequestApproved) {
			return server.ApproveEnrollmentRequest400JSONResponse{Message: "Enrollment request is already approved"}, nil
		}
		if request.Body.ApprovedAt != nil {
			return server.ApproveEnrollmentRequest400JSONResponse{Message: "ApprovedAt is not allowed to be set when approving enrollment requests"}, nil
		}
		request.Body.ApprovedAt = util.TimeToPtr(time.Now())

		// The same check should happen for ApprovedBy, but we don't have a way to identify
		// users yet, so we'll let the UI set it for now.
		if request.Body.ApprovedBy == nil {
			request.Body.ApprovedBy = util.StrToPtr("unknown")
		}

		if err := approveAndSignEnrollmentRequest(h.ca, enrollmentReq, request.Body); err != nil {
			return server.ApproveEnrollmentRequest400JSONResponse{Message: fmt.Sprintf("Error approving and signing enrollment request: %v", err.Error())}, nil
		}

		// in case of error we return 500 as it will be caused by creating device in db and not by problem with enrollment request
		if err := h.createDeviceFromEnrollmentRequest(ctx, orgId, enrollmentReq); err != nil {
			return nil, fmt.Errorf("Error creating device from enrollment request: %v", err.Error())
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
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "enrollmentrequests/status", "update")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.ReplaceEnrollmentRequestStatus503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.ReplaceEnrollmentRequestStatus403JSONResponse{Message: Forbidden}, nil
	}
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

// (PATCH /api/v1/enrollmentrequests/{name}/status)
func (h *ServiceHandler) PatchEnrollmentRequestStatus(ctx context.Context, request server.PatchEnrollmentRequestStatusRequestObject) (server.PatchEnrollmentRequestStatusResponseObject, error) {
	return nil, fmt.Errorf("not yet implemented")
}
