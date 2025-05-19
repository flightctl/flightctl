package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	authcommon "github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/google/uuid"
	"github.com/samber/lo"
)

const ClientCertExpiryDays = 365

func approveEnrollmentRequest(ca *crypto.CAClient, enrollmentRequest *api.EnrollmentRequest, approval *api.EnrollmentRequestApprovalStatus) error {
	if enrollmentRequest == nil {
		return errors.New("approveEnrollmentRequest: enrollmentRequest is nil")
	}

	if enrollmentRequest.Metadata.Name == nil {
		return fmt.Errorf("approveEnrollmentRequest: enrollment request is missing metadata.name")
	}

	enrollmentRequest.Status = &api.EnrollmentRequestStatus{
		Conditions:  []api.Condition{},
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
	condition := api.Condition{
		Type:    api.EnrollmentRequestApproved,
		Status:  api.ConditionStatusTrue,
		Reason:  "ManuallyApproved",
		Message: "Approved by " + approval.ApprovedBy,
	}
	api.SetStatusCondition(&enrollmentRequest.Status.Conditions, condition)
	return nil
}

func signEnrollmentRequest(ca *crypto.CAClient, enrollmentRequest *api.EnrollmentRequest) error {
	if enrollmentRequest == nil {
		return errors.New("signEnrollmentRequest: enrollmentRequest is nil")
	}

	csr, err := crypto.ParseCSR([]byte(enrollmentRequest.Spec.Csr))
	if err != nil {
		return fmt.Errorf("signEnrollmentRequest: error parsing CSR: %w", err)
	}

	supplied, err := ca.CNFromDeviceFingerprint(csr.Subject.CommonName)
	if err != nil {
		return fmt.Errorf("signEnrollmentRequest: invalid CN supplied in CSR: %w", err)
	}

	desired, err := ca.CNFromDeviceFingerprint(*enrollmentRequest.Metadata.Name)
	if err != nil {
		return fmt.Errorf("signEnrollmentRequest: error setting CN in CSR: %w", err)
	}

	if desired != supplied {
		return fmt.Errorf("signEnrollmentRequest: attempt to supply a fake CN, possible identity theft, csr: %s, metadata %s", supplied, desired)
	}
	csr.Subject.CommonName = desired

	if err := csr.CheckSignature(); err != nil {
		return fmt.Errorf("failed to verify signature of CSR: %w", err)
	}

	expirySeconds := ClientCertExpiryDays * 24 * 60 * 60
	certData, err := ca.IssueRequestedClientCertificate(csr, expirySeconds)
	if err != nil {
		return err
	}

	enrollmentRequest.Status.Certificate = lo.ToPtr(string(certData))
	return nil
}

func AddStatusIfNeeded(enrollmentRequest *api.EnrollmentRequest) {
	if enrollmentRequest.Status == nil {
		enrollmentRequest.Status = &api.EnrollmentRequestStatus{
			Certificate: nil,
			Conditions:  []api.Condition{},
		}
	}
}

func (h *ServiceHandler) createDeviceFromEnrollmentRequest(ctx context.Context, orgId uuid.UUID, enrollmentRequest *api.EnrollmentRequest) error {
	status := api.NewDeviceStatus()
	status.Lifecycle = api.DeviceLifecycleStatus{Status: "Enrolled"}
	apiResource := &api.Device{
		Metadata: api.ObjectMeta{
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

func (h *ServiceHandler) CreateEnrollmentRequest(ctx context.Context, er api.EnrollmentRequest) (*api.EnrollmentRequest, api.Status) {
	orgId := store.NullOrgId

	// don't set fields that are managed by the service
	er.Status = nil
	NilOutManagedObjectMetaProperties(&er.Metadata)

	if errs := er.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}
	AddStatusIfNeeded(&er)

	result, err := h.store.EnrollmentRequest().Create(ctx, orgId, &er)
	status := StoreErrorToApiStatus(err, true, api.EnrollmentRequestKind, er.Metadata.Name)
	h.CreateEvent(ctx, GetResourceCreatedOrUpdatedEvent(ctx, true, api.EnrollmentRequestKind, *er.Metadata.Name, status, nil))
	return result, status
}

func (h *ServiceHandler) ListEnrollmentRequests(ctx context.Context, params api.ListEnrollmentRequestsParams) (*api.EnrollmentRequestList, api.Status) {
	orgId := store.NullOrgId

	listParams, status := prepareListParams(params.Continue, params.LabelSelector, params.FieldSelector, params.Limit)
	if status != api.StatusOK() {
		return nil, status
	}

	result, err := h.store.EnrollmentRequest().List(ctx, orgId, *listParams)
	if err == nil {
		return result, api.StatusOK()
	}

	var se *selector.SelectorError

	switch {
	case selector.AsSelectorError(err, &se):
		return nil, api.StatusBadRequest(se.Error())
	default:
		return nil, api.StatusInternalServerError(err.Error())
	}
}

func (h *ServiceHandler) DeleteEnrollmentRequests(ctx context.Context) api.Status {
	orgId := store.NullOrgId

	err := h.store.EnrollmentRequest().DeleteAll(ctx, orgId)
	return StoreErrorToApiStatus(err, false, api.EnrollmentRequestKind, nil)
}

func (h *ServiceHandler) GetEnrollmentRequest(ctx context.Context, name string) (*api.EnrollmentRequest, api.Status) {
	orgId := store.NullOrgId

	result, err := h.store.EnrollmentRequest().Get(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, api.EnrollmentRequestKind, &name)
}

func (h *ServiceHandler) ReplaceEnrollmentRequest(ctx context.Context, name string, er api.EnrollmentRequest) (*api.EnrollmentRequest, api.Status) {
	orgId := store.NullOrgId

	// don't set fields that are managed by the service
	er.Status = nil
	NilOutManagedObjectMetaProperties(&er.Metadata)

	if errs := er.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}
	if name != *er.Metadata.Name {
		return nil, api.StatusBadRequest("resource name specified in metadata does not match name in path")
	}

	AddStatusIfNeeded(&er)

	result, created, updateDesc, err := h.store.EnrollmentRequest().CreateOrUpdate(ctx, orgId, &er)
	status := StoreErrorToApiStatus(err, created, api.EnrollmentRequestKind, &name)
	h.CreateEvent(ctx, GetResourceCreatedOrUpdatedEvent(ctx, created, api.EnrollmentRequestKind, name, status, &updateDesc))
	return result, status
}

// Only metadata.labels and spec can be patched. If we try to patch other fields, HTTP 400 Bad Request is returned.
func (h *ServiceHandler) PatchEnrollmentRequest(ctx context.Context, name string, patch api.PatchRequest) (*api.EnrollmentRequest, api.Status) {
	orgId := store.NullOrgId

	currentObj, err := h.store.EnrollmentRequest().Get(ctx, orgId, name)
	if err != nil {
		return nil, StoreErrorToApiStatus(err, false, api.EnrollmentRequestKind, &name)
	}

	newObj := &api.EnrollmentRequest{}
	err = ApplyJSONPatch(ctx, currentObj, newObj, patch, "/api/v1/enrollmentrequests/"+name)
	if err != nil {
		return nil, api.StatusBadRequest(err.Error())
	}

	if errs := newObj.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}
	if newObj.Metadata.Name == nil || *currentObj.Metadata.Name != *newObj.Metadata.Name {
		return nil, api.StatusBadRequest("metadata.name is immutable")
	}
	if currentObj.ApiVersion != newObj.ApiVersion {
		return nil, api.StatusBadRequest("apiVersion is immutable")
	}
	if currentObj.Kind != newObj.Kind {
		return nil, api.StatusBadRequest("kind is immutable")
	}
	if !reflect.DeepEqual(currentObj.Status, newObj.Status) {
		return nil, api.StatusBadRequest("status is immutable")
	}

	NilOutManagedObjectMetaProperties(&newObj.Metadata)
	newObj.Metadata.ResourceVersion = nil

	result, updateDesc, err := h.store.EnrollmentRequest().Update(ctx, orgId, newObj)
	status := StoreErrorToApiStatus(err, false, api.EnrollmentRequestKind, &name)
	h.CreateEvent(ctx, GetResourceCreatedOrUpdatedEvent(ctx, false, api.EnrollmentRequestKind, name, status, &updateDesc))
	return result, status
}

func (h *ServiceHandler) DeleteEnrollmentRequest(ctx context.Context, name string) api.Status {
	orgId := store.NullOrgId

	err := h.store.EnrollmentRequest().Delete(ctx, orgId, name)
	status := StoreErrorToApiStatus(err, false, api.EnrollmentRequestKind, &name)
	h.CreateEvent(ctx, GetResourceDeletedEvent(ctx, api.EnrollmentRequestKind, name, status))
	return status
}

func (h *ServiceHandler) GetEnrollmentRequestStatus(ctx context.Context, name string) (*api.EnrollmentRequest, api.Status) {
	orgId := store.NullOrgId

	result, err := h.store.EnrollmentRequest().Get(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, api.EnrollmentRequestKind, &name)
}

func (h *ServiceHandler) ApproveEnrollmentRequest(ctx context.Context, name string, approval api.EnrollmentRequestApproval) (*api.EnrollmentRequestApprovalStatus, api.Status) {
	orgId := store.NullOrgId

	if errs := approval.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}
	enrollmentReq, err := h.store.EnrollmentRequest().Get(ctx, orgId, name)
	if err != nil {
		return nil, StoreErrorToApiStatus(err, false, api.EnrollmentRequestKind, &name)
	}

	approvalStatusToReturn := enrollmentReq.Status.Approval

	// if the enrollment request was already approved we should not try to approve it one more time
	if approval.Approved {
		if api.IsStatusConditionTrue(enrollmentReq.Status.Conditions, api.EnrollmentRequestApproved) {
			return nil, api.StatusBadRequest("Enrollment request is already approved")
		}

		identity, err := authcommon.GetIdentity(ctx)
		if err != nil {
			return nil, api.StatusInternalServerError(fmt.Sprintf("failed to retrieve user identity while approving enrollment request: %v", err))
		}

		approvedBy := "unknown"
		if identity != nil && len(identity.Username) > 0 {
			approvedBy = identity.Username
		}

		approvalStatus := api.EnrollmentRequestApprovalStatus{
			Approved:   approval.Approved,
			Labels:     approval.Labels,
			ApprovedAt: time.Now(),
			ApprovedBy: approvedBy,
		}
		approvalStatusToReturn = &approvalStatus

		if err := approveEnrollmentRequest(h.ca, enrollmentReq, &approvalStatus); err != nil {
			return nil, api.StatusBadRequest(fmt.Sprintf("Error approving and signing enrollment request: %v", err.Error()))
		}
		if err := signEnrollmentRequest(h.ca, enrollmentReq); err != nil {
			return nil, api.StatusBadRequest(fmt.Sprintf("Error approving and signing enrollment request: %v", err.Error()))
		}

		// in case of error we return 500 as it will be caused by creating device in db and not by problem with enrollment request
		if err := h.createDeviceFromEnrollmentRequest(ctx, orgId, enrollmentReq); err != nil {
			return nil, api.StatusInternalServerError(fmt.Sprintf("error creating device from enrollment request: %v", err))
		}
	}
	_, err = h.store.EnrollmentRequest().UpdateStatus(ctx, orgId, enrollmentReq)
	return approvalStatusToReturn, StoreErrorToApiStatus(err, false, api.EnrollmentRequestKind, &name)
}

func (h *ServiceHandler) ReplaceEnrollmentRequestStatus(ctx context.Context, name string, er api.EnrollmentRequest) (*api.EnrollmentRequest, api.Status) {
	orgId := store.NullOrgId

	AddStatusIfNeeded(&er)

	result, err := h.store.EnrollmentRequest().UpdateStatus(ctx, orgId, &er)
	return result, StoreErrorToApiStatus(err, false, api.EnrollmentRequestKind, &name)
}
