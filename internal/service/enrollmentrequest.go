package service

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	authcommon "github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/crypto/signer"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/tpm"
	"github.com/google/uuid"
	"github.com/samber/lo"
)

// getTPMCAPool loads the TPM CA certificates from configured paths
func (h *ServiceHandler) getTPMCAPool() *x509.CertPool {
	if len(h.tpmCAPaths) == 0 {
		return nil
	}

	roots, err := tpm.LoadCAsFromPaths(h.tpmCAPaths)
	if err != nil {
		h.log.Warnf("Failed to load TPM CA certificates from configured paths: %v", err)
		return nil
	}

	return roots
}

func (h *ServiceHandler) verifyTPMEnrollmentRequest(er *api.EnrollmentRequest, name string) error {
	csrBytes, isTPM := tpm.ParseTCGCSRBytes(er.Spec.Csr)
	if !isTPM {
		return fmt.Errorf("failed to parse TCG CSR")
	}

	trustedRoots := h.getTPMCAPool()
	if err := tpm.VerifyTCGCSRChainOfTrustWithRoots(csrBytes, trustedRoots); err != nil {
		condition := api.Condition{
			Type:    "TPMVerified",
			Status:  api.ConditionStatusFalse,
			Reason:  "TPMVerificationFailed",
			Message: err.Error(),
		}
		api.SetStatusCondition(&er.Status.Conditions, condition)
		h.log.Warnf("TPM verification failed for enrollment request %s: %v", name, err)
	} else {
		condition := api.Condition{
			Type:    "TPMVerified",
			Status:  api.ConditionStatusTrue,
			Reason:  "TPMVerificationSucceeded",
			Message: "TPM chain of trust verified successfully",
		}
		api.SetStatusCondition(&er.Status.Conditions, condition)
		h.log.Debugf("TPM verification passed for enrollment request %s", name)
	}
	return nil
}

func approveAndSignEnrollmentRequest(ctx context.Context, ca *crypto.CAClient, enrollmentRequest *api.EnrollmentRequest, approval *api.EnrollmentRequestApprovalStatus) error {
	if enrollmentRequest == nil {
		return errors.New("approveAndSignEnrollmentRequest: enrollmentRequest is nil")
	}

	request, _, err := newSignRequestFromEnrollment(ca.Cfg, enrollmentRequest)
	if err != nil {
		return fmt.Errorf("approveAndSignEnrollmentRequest: %w", err)
	}

	certData, err := signer.SignAsPEM(ctx, ca, request)
	if err != nil {
		return fmt.Errorf("approveAndSignEnrollmentRequest: %w", err)
	}

	// preserve existing conditions when approving
	existingConditions := []api.Condition{}
	if enrollmentRequest.Status != nil && enrollmentRequest.Status.Conditions != nil {
		existingConditions = enrollmentRequest.Status.Conditions
	}

	enrollmentRequest.Status = &api.EnrollmentRequestStatus{
		Certificate: lo.ToPtr(string(certData)),
		Conditions:  existingConditions,
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
		Type:    api.ConditionTypeEnrollmentRequestApproved,
		Status:  api.ConditionStatusTrue,
		Reason:  "ManuallyApproved",
		Message: "Approved by " + approval.ApprovedBy,
	}
	api.SetStatusCondition(&enrollmentRequest.Status.Conditions, condition)
	return nil
}

func addStatusIfNeeded(enrollmentRequest *api.EnrollmentRequest) {
	if enrollmentRequest.Status == nil {
		enrollmentRequest.Status = &api.EnrollmentRequestStatus{
			Certificate: nil,
			Conditions:  []api.Condition{},
		}
	}
}

func (h *ServiceHandler) createDeviceFromEnrollmentRequest(ctx context.Context, orgId uuid.UUID, enrollmentRequest *api.EnrollmentRequest) error {
	deviceStatus := api.NewDeviceStatus()
	deviceStatus.Lifecycle = api.DeviceLifecycleStatus{Status: "Enrolled"}

	// Check if TPM was verified during enrollment request creation
	isTPMVerified := false
	var tpmVerificationError string
	if enrollmentRequest.Status != nil {
		if condition := api.FindStatusCondition(enrollmentRequest.Status.Conditions, "TPMVerified"); condition != nil {
			isTPMVerified = (condition.Status == api.ConditionStatusTrue)
			if !isTPMVerified {
				tpmVerificationError = condition.Message
			}
		}
	}

	// always set device integrity status based on TPM verification result
	now := time.Now()
	if isTPMVerified {
		deviceStatus.Integrity = api.DeviceIntegrityStatus{
			Status:       api.DeviceIntegrityStatusVerified,
			Info:         lo.ToPtr("All integrity checks completed successfully"),
			LastVerified: &now,
			DeviceIdentity: &api.DeviceIntegrityCheckStatus{
				Status: api.DeviceIntegrityCheckStatusVerified,
				Info:   lo.ToPtr("Device identity verified through certificate chain"),
			},
			Tpm: &api.DeviceIntegrityCheckStatus{
				Status: api.DeviceIntegrityCheckStatusVerified,
				Info:   lo.ToPtr("TPM chain of trust verified"),
			},
		}
	} else {
		// Check if this was a TCG CSR enrollment attempt that failed verification
		csrBytes := []byte(enrollmentRequest.Spec.Csr)

		if !tpm.IsTCGCSRFormat(csrBytes) {
			decodedBytes, err := base64.StdEncoding.DecodeString(enrollmentRequest.Spec.Csr)
			if err == nil && tpm.IsTCGCSRFormat(decodedBytes) {
				csrBytes = decodedBytes
			}
		}

		if tpm.IsTCGCSRFormat(csrBytes) {
			tpmErrorMsg := "TPM attestation verification failed"
			deviceIdentityMsg := "Device identity verification failed"

			if tpmVerificationError != "" {
				if strings.Contains(tpmVerificationError, "TPM CA certificates not configured") {
					tpmErrorMsg = "TPM CA certificates not configured - cannot verify chain"
					deviceIdentityMsg = "Cannot verify identity - TPM CA certificates not configured"
				} else {
					tpmErrorMsg = fmt.Sprintf("TPM verification failed: %s", tpmVerificationError)
					deviceIdentityMsg = fmt.Sprintf("Identity verification failed: %s", tpmVerificationError)
				}
			}

			deviceStatus.Integrity = api.DeviceIntegrityStatus{
				Status:       api.DeviceIntegrityStatusFailed,
				Info:         lo.ToPtr("Integrity verification failed"),
				LastVerified: &now,
				DeviceIdentity: &api.DeviceIntegrityCheckStatus{
					Status: api.DeviceIntegrityCheckStatusFailed,
					Info:   lo.ToPtr(deviceIdentityMsg),
				},
				Tpm: &api.DeviceIntegrityCheckStatus{
					Status: api.DeviceIntegrityCheckStatusFailed,
					Info:   lo.ToPtr(tpmErrorMsg),
				},
			}
		}
	}

	name := lo.FromPtr(enrollmentRequest.Metadata.Name)
	apiResource := &api.Device{
		Metadata: api.ObjectMeta{
			Name: &name,
		},
		Status: &deviceStatus,
	}
	if errs := apiResource.Validate(); len(errs) > 0 {
		return fmt.Errorf("failed validating new device: %w", errors.Join(errs...))
	}
	if enrollmentRequest.Status.Approval != nil {
		apiResource.Metadata.Labels = enrollmentRequest.Status.Approval.Labels
	}
	common.UpdateServiceSideStatus(ctx, orgId, apiResource, h.store, h.log)

	_, err := h.store.Device().Create(ctx, orgId, apiResource, h.callbackDeviceUpdated)
	return err
}

func (h *ServiceHandler) CreateEnrollmentRequest(ctx context.Context, er api.EnrollmentRequest) (*api.EnrollmentRequest, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	// don't set fields that are managed by the service
	er.Status = nil
	addStatusIfNeeded(&er)

	if errs := er.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}

	request, isTPM, err := newSignRequestFromEnrollment(h.ca.Cfg, &er)
	if err != nil {
		return nil, api.StatusBadRequest(err.Error())
	}
	if err := signer.Verify(ctx, h.ca, request); err != nil {
		return nil, api.StatusBadRequest(err.Error())
	}
	if isTPM {
		if err := h.verifyTPMEnrollmentRequest(&er, *er.Metadata.Name); err != nil {
			return nil, api.StatusBadRequest(err.Error())
		}
	}

	result, err := h.store.EnrollmentRequest().Create(ctx, orgId, &er, h.callbackEnrollmentRequestUpdated)
	return result, StoreErrorToApiStatus(err, true, api.EnrollmentRequestKind, er.Metadata.Name)
}

func (h *ServiceHandler) ListEnrollmentRequests(ctx context.Context, params api.ListEnrollmentRequestsParams) (*api.EnrollmentRequestList, api.Status) {
	orgId := getOrgIdFromContext(ctx)

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

func (h *ServiceHandler) GetEnrollmentRequest(ctx context.Context, name string) (*api.EnrollmentRequest, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	result, err := h.store.EnrollmentRequest().Get(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, api.EnrollmentRequestKind, &name)
}

func (h *ServiceHandler) ReplaceEnrollmentRequest(ctx context.Context, name string, er api.EnrollmentRequest) (*api.EnrollmentRequest, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	// don't set fields that are managed by the service
	er.Status = nil
	addStatusIfNeeded(&er)
	NilOutManagedObjectMetaProperties(&er.Metadata)

	if errs := er.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}
	err := h.allowCreationOrUpdate(ctx, orgId, name)
	if err != nil {
		return nil, api.StatusBadRequest(err.Error())
	}
	if name != *er.Metadata.Name {
		return nil, api.StatusBadRequest("resource name specified in metadata does not match name in path")
	}

	request, isTPM, err := newSignRequestFromEnrollment(h.ca.Cfg, &er)
	if err != nil {
		return nil, api.StatusBadRequest(err.Error())
	}
	if err := signer.Verify(ctx, h.ca, request); err != nil {
		return nil, api.StatusBadRequest(err.Error())
	}
	if isTPM {
		if err := h.verifyTPMEnrollmentRequest(&er, name); err != nil {
			return nil, api.StatusBadRequest(err.Error())
		}
	}

	result, created, err := h.store.EnrollmentRequest().CreateOrUpdate(ctx, orgId, &er, h.callbackEnrollmentRequestUpdated)
	return result, StoreErrorToApiStatus(err, created, api.EnrollmentRequestKind, &name)
}

// Only metadata.labels and spec can be patched. If we try to patch other fields, HTTP 400 Bad Request is returned.
func (h *ServiceHandler) PatchEnrollmentRequest(ctx context.Context, name string, patch api.PatchRequest) (*api.EnrollmentRequest, api.Status) {
	orgId := getOrgIdFromContext(ctx)

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
	if errs := currentObj.ValidateUpdate(newObj); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}

	NilOutManagedObjectMetaProperties(&newObj.Metadata)
	newObj.Metadata.ResourceVersion = nil

	request, isTPM, err := newSignRequestFromEnrollment(h.ca.Cfg, newObj)
	if err != nil {
		return nil, api.StatusBadRequest(err.Error())
	}
	if err := signer.Verify(ctx, h.ca, request); err != nil {
		return nil, api.StatusBadRequest(err.Error())
	}
	if isTPM {
		if err := h.verifyTPMEnrollmentRequest(newObj, name); err != nil {
			return nil, api.StatusBadRequest(err.Error())
		}
	}

	result, err := h.store.EnrollmentRequest().Update(ctx, orgId, newObj, h.callbackEnrollmentRequestUpdated)
	return result, StoreErrorToApiStatus(err, false, api.EnrollmentRequestKind, &name)
}

func (h *ServiceHandler) DeleteEnrollmentRequest(ctx context.Context, name string) api.Status {
	orgId := getOrgIdFromContext(ctx)

	exists, err := h.deviceExists(ctx, name)
	if err != nil {
		return StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
	}

	if exists {
		return api.StatusConflict(fmt.Sprintf("cannot delete ER %q: device exists", name))
	}

	err = h.store.EnrollmentRequest().Delete(ctx, orgId, name, h.callbackEnrollmentRequestDeleted)
	return StoreErrorToApiStatus(err, false, api.EnrollmentRequestKind, &name)
}

func (h *ServiceHandler) GetEnrollmentRequestStatus(ctx context.Context, name string) (*api.EnrollmentRequest, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	result, err := h.store.EnrollmentRequest().Get(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, api.EnrollmentRequestKind, &name)
}

func (h *ServiceHandler) ApproveEnrollmentRequest(ctx context.Context, name string, approval api.EnrollmentRequestApproval) (*api.EnrollmentRequestApprovalStatus, api.Status) {
	orgId := getOrgIdFromContext(ctx)

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
		if api.IsStatusConditionTrue(enrollmentReq.Status.Conditions, api.ConditionTypeEnrollmentRequestApproved) {
			return nil, api.StatusBadRequest("Enrollment request is already approved")
		}

		identity, err := authcommon.GetIdentity(ctx)
		if err != nil {
			status := api.StatusInternalServerError(fmt.Sprintf("failed to retrieve user identity while approving enrollment request: %v", err))
			h.CreateEvent(ctx, common.GetEnrollmentRequestApprovalFailedEvent(ctx, name, status, h.log))
			return nil, status
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

		err = approveAndSignEnrollmentRequest(ctx, h.ca, enrollmentReq, &approvalStatus)
		if err != nil {
			status := api.StatusBadRequest(fmt.Sprintf("Error approving and signing enrollment request: %v", err.Error()))
			h.CreateEvent(ctx, common.GetEnrollmentRequestApprovalFailedEvent(ctx, name, status, h.log))
			return nil, status
		}

		// in case of error we return 500 as it will be caused by creating device in db and not by problem with enrollment request
		if err := h.createDeviceFromEnrollmentRequest(ctx, orgId, enrollmentReq); err != nil {
			status := api.StatusInternalServerError(fmt.Sprintf("error creating device from enrollment request: %v", err))
			h.CreateEvent(ctx, common.GetEnrollmentRequestApprovalFailedEvent(ctx, name, status, h.log))
			return nil, status
		}
	}

	// Update the enrollment request status using the specific approval callback
	_, err = h.store.EnrollmentRequest().UpdateStatus(ctx, orgId, enrollmentReq, h.callbackEnrollmentRequestApproved)
	return approvalStatusToReturn, StoreErrorToApiStatus(err, false, api.EnrollmentRequestKind, &name)
}

func (h *ServiceHandler) ReplaceEnrollmentRequestStatus(ctx context.Context, name string, er api.EnrollmentRequest) (*api.EnrollmentRequest, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	addStatusIfNeeded(&er)

	result, err := h.store.EnrollmentRequest().UpdateStatus(ctx, orgId, &er, h.callbackEnrollmentRequestUpdated)
	return result, StoreErrorToApiStatus(err, false, api.EnrollmentRequestKind, &name)
}

func newSignRequestFromEnrollment(cfg *ca.Config, er *api.EnrollmentRequest) (signer.SignRequest, bool, error) {
	csrData, isTPM, err := tpm.NormalizeEnrollmentCSR(er.Spec.Csr)
	if err != nil {
		return nil, false, fmt.Errorf("failed to normalize CSR: %w", err)
	}

	var opts []signer.SignRequestOption
	if er.Status != nil && er.Status.Certificate != nil {
		certBytes := []byte(*er.Status.Certificate)
		opts = append(opts, signer.WithIssuedCertificateBytes(certBytes))
	}

	if er.Metadata.Name != nil {
		opts = append(opts, signer.WithResourceName(*er.Metadata.Name))
	}

	request, err := signer.NewSignRequestFromBytes(cfg.DeviceEnrollmentSignerName, csrData, opts...)

	if err != nil {
		return nil, isTPM, err
	}

	return request, isTPM, nil
}

func (h *ServiceHandler) allowCreationOrUpdate(ctx context.Context, orgId uuid.UUID, name string) error {
	device, err := h.store.Device().Get(ctx, orgId, name)
	if errors.Is(err, flterrors.ErrResourceNotFound) {
		return nil // Device not found: allow to create or update
	}
	if device != nil {
		return flterrors.ErrDuplicateName // Duplicate name: creation blocked
	}
	return err
}

// deviceExists checks if a device with the given name exists in the store.
// Error is returned if there is an error other than ErrResourceNotFound.
func (h *ServiceHandler) deviceExists(ctx context.Context, name string) (bool, error) {
	dev, err := h.store.Device().Get(ctx, store.NullOrgId, name)
	if errors.Is(err, flterrors.ErrResourceNotFound) {
		return false, nil
	}
	return dev != nil, err
}

// callbackEnrollmentRequestUpdated is the enrollment request-specific callback that handles enrollment request events
func (h *ServiceHandler) callbackEnrollmentRequestUpdated(ctx context.Context, resourceKind api.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	h.eventHandler.HandleEnrollmentRequestUpdatedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
}

// callbackEnrollmentRequestDeleted is the enrollment request-specific callback that handles enrollment request deletion events
func (h *ServiceHandler) callbackEnrollmentRequestDeleted(ctx context.Context, resourceKind api.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	h.eventHandler.HandleGenericResourceDeletedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
}

// callbackEnrollmentRequestApproved is the enrollment request-specific callback that handles enrollment request approval events
func (h *ServiceHandler) callbackEnrollmentRequestApproved(ctx context.Context, resourceKind api.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	h.eventHandler.HandleEnrollmentRequestApprovedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
}
