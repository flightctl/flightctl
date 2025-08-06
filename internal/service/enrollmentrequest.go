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
	"github.com/flightctl/flightctl/internal/crypto"
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

// handleTPMEnrollmentRequest checks if the enrollment request contains a TCG
// CSR and if so, verifies it and stores the verification status in labels.
// Returns true if this is a TCG CSR, false for standard CSRs.
func (h *ServiceHandler) handleTPMEnrollmentRequest(er *api.EnrollmentRequest, name string) bool {
	csrBytes := []byte(er.Spec.Csr)

	if !tpm.IsTCGCSRFormat(csrBytes) {
		// try to decode as base64 - it might be an encoded TCG CSR
		decodedBytes, err := base64.StdEncoding.DecodeString(er.Spec.Csr)
		if err == nil && tpm.IsTCGCSRFormat(decodedBytes) {
			csrBytes = decodedBytes
		}
	}

	if !tpm.IsTCGCSRFormat(csrBytes) {
		// standard csr verification
		return false
	}

	// perform verification and store results in labels
	h.verifyAndLabelTPMEnrollment(er, csrBytes, name)
	return true
}

// verifyAndLabelTPMEnrollment verifies a TCG CSR and stores the verification status in conditions.
// This method always allows enrollment to proceed, storing any errors in conditions for admin review.
func (h *ServiceHandler) verifyAndLabelTPMEnrollment(er *api.EnrollmentRequest, csrBytes []byte, name string) {
	trustedRoots := h.getTPMCAPool()

	// Ensure status exists
	addStatusIfNeeded(er)

	if err := tpm.VerifyTCGCSRChainOfTrustWithRoots(csrBytes, trustedRoots); err != nil {
		// Store verification failure in condition
		condition := api.Condition{
			Type:    "TPMVerified",
			Status:  api.ConditionStatusFalse,
			Reason:  "TPMVerificationFailed",
			Message: err.Error(),
		}
		api.SetStatusCondition(&er.Status.Conditions, condition)
		h.log.Warnf("TPM verification failed for enrollment request %s: %v", name, err)
	} else {
		// Store verification success in condition
		condition := api.Condition{
			Type:    "TPMVerified",
			Status:  api.ConditionStatusTrue,
			Reason:  "TPMVerificationSucceeded",
			Message: "TPM attestation chain of trust verified successfully",
		}
		api.SetStatusCondition(&er.Status.Conditions, condition)
		h.log.Debugf("TPM verification passed for enrollment request %s", name)
	}
}

func approveAndSignEnrollmentRequest(ctx context.Context, ca *crypto.CAClient, enrollmentRequest *api.EnrollmentRequest, approval *api.EnrollmentRequestApprovalStatus) error {
	if enrollmentRequest == nil {
		return errors.New("approveAndSignEnrollmentRequest: enrollmentRequest is nil")
	}

	var certData []byte
	var err error

	csrBytes := []byte(enrollmentRequest.Spec.Csr)
	if !tpm.IsTCGCSRFormat(csrBytes) {
		// try to decode as base64 - it might be an encoded TCG CSR
		decodedBytes, err := base64.StdEncoding.DecodeString(enrollmentRequest.Spec.Csr)
		if err == nil && tpm.IsTCGCSRFormat(decodedBytes) {
			// successfully decoded and it's a TCG CSR
			csrBytes = decodedBytes
		}
	}

	if tpm.IsTCGCSRFormat(csrBytes) {
		parsed, err := tpm.ParseTCGCSR(csrBytes)
		if err != nil {
			return fmt.Errorf("approveAndSignEnrollmentRequest: failed to parse TCG CSR: %w", err)
		}

		tpmData, err := tpm.ExtractTPMDataFromTCGCSR(parsed)
		if err != nil {
			return fmt.Errorf("approveAndSignEnrollmentRequest: failed to extract TPM data: %w", err)
		}

		// for TCG CSR, we need to use the embedded standard X.509 CSR for signing
		if len(tpmData.StandardCSR) == 0 {
			return fmt.Errorf("approveAndSignEnrollmentRequest: TCG CSR does not contain embedded X.509 CSR")
		}

		// temporarily replace the enrollment request CSR with the embedded standard CSR
		originalCSR := enrollmentRequest.Spec.Csr
		enrollmentRequest.Spec.Csr = string(tpmData.StandardCSR)

		csr := enrollmentRequestToCSR(ca, enrollmentRequest)
		signer := ca.GetSigner(csr.Spec.SignerName)
		if signer == nil {
			enrollmentRequest.Spec.Csr = originalCSR
			return fmt.Errorf("approveAndSignEnrollmentRequest: signer %q not found", csr.Spec.SignerName)
		}

		certData, err = signer.Sign(ctx, csr)

		// restore original CSR
		enrollmentRequest.Spec.Csr = originalCSR

		if err != nil {
			return fmt.Errorf("approveAndSignEnrollmentRequest: %w", err)
		}
	} else {
		// standard CSR signing flow
		csr := enrollmentRequestToCSR(ca, enrollmentRequest)
		signer := ca.GetSigner(csr.Spec.SignerName)
		if signer == nil {
			return fmt.Errorf("approveAndSignEnrollmentRequest: signer %q not found", csr.Spec.SignerName)
		}

		certData, err = signer.Sign(ctx, csr)
		if err != nil {
			return fmt.Errorf("approveAndSignEnrollmentRequest: %w", err)
		}
	}

	enrollmentRequest.Status = &api.EnrollmentRequestStatus{
		Certificate: lo.ToPtr(string(certData)),
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
				Info:   lo.ToPtr("TPM attestation chain of trust verified"),
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

	_, err := h.store.Device().Create(ctx, orgId, apiResource, h.callbackManager.DeviceUpdatedCallback, h.callbackDeviceUpdated)
	return err
}

func (h *ServiceHandler) CreateEnrollmentRequest(ctx context.Context, er api.EnrollmentRequest) (*api.EnrollmentRequest, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	// don't set fields that are managed by the service
	er.Status = nil

	if errs := er.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}

	// Check and handle TPM-based enrollment requests
	if !h.handleTPMEnrollmentRequest(&er, *er.Metadata.Name) {
		csr := enrollmentRequestToCSR(h.ca, &er)
		signer := h.ca.GetSigner(csr.Spec.SignerName)
		if signer == nil {
			return nil, api.StatusBadRequest(fmt.Sprintf("signer %q not found", csr.Spec.SignerName))
		}

		if err := signer.Verify(ctx, csr); err != nil {
			return nil, api.StatusBadRequest(err.Error())
		}
	}

	addStatusIfNeeded(&er)

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

	// Check and handle TPM-based enrollment requests
	if !h.handleTPMEnrollmentRequest(&er, name) {
		// standard CSR verification flow
		csr := enrollmentRequestToCSR(h.ca, &er)
		signer := h.ca.GetSigner(csr.Spec.SignerName)
		if signer == nil {
			return nil, api.StatusBadRequest(fmt.Sprintf("signer %q not found", csr.Spec.SignerName))
		}

		if err := signer.Verify(ctx, csr); err != nil {
			return nil, api.StatusBadRequest(err.Error())
		}
	}

	addStatusIfNeeded(&er)

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

	// Check and handle TPM-based enrollment requests
	if !h.handleTPMEnrollmentRequest(newObj, name) {
		// Standard CSR verification flow
		csr := enrollmentRequestToCSR(h.ca, newObj)
		signer := h.ca.GetSigner(csr.Spec.SignerName)
		if signer == nil {
			return nil, api.StatusBadRequest(fmt.Sprintf("signer %q not found", csr.Spec.SignerName))
		}

		if err := signer.Verify(ctx, csr); err != nil {
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

func enrollmentRequestToCSR(ca *crypto.CAClient, enrollmentRequest *api.EnrollmentRequest) api.CertificateSigningRequest {
	return api.CertificateSigningRequest{
		ApiVersion: "v1alpha1",
		Kind:       "CertificateSigningRequest",
		Metadata: api.ObjectMeta{
			Name: enrollmentRequest.Metadata.Name,
		},
		Spec: api.CertificateSigningRequestSpec{
			Request:    []byte(enrollmentRequest.Spec.Csr),
			SignerName: ca.Cfg.DeviceEnrollmentSignerName,
		},
	}
}

// callbackEnrollmentRequestUpdated is the enrollment request-specific callback that handles enrollment request events
func (h *ServiceHandler) callbackEnrollmentRequestUpdated(ctx context.Context, resourceKind api.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	h.HandleGenericResourceUpdatedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
}

// callbackEnrollmentRequestDeleted is the enrollment request-specific callback that handles enrollment request deletion events
func (h *ServiceHandler) callbackEnrollmentRequestDeleted(ctx context.Context, resourceKind api.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	h.HandleGenericResourceDeletedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
}

// callbackEnrollmentRequestApproved is the enrollment request-specific callback that handles enrollment request approval events
func (h *ServiceHandler) callbackEnrollmentRequestApproved(ctx context.Context, resourceKind api.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	h.HandleEnrollmentRequestApprovedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
}
