package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/crypto/signer"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/tpm"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
)

// nowFunc allows overriding for unit tests
var nowFunc = time.Now

func (h *ServiceHandler) autoApprove(ctx context.Context, orgId uuid.UUID, csr *api.CertificateSigningRequest) {
	if api.IsStatusConditionTrue(csr.Status.Conditions, api.ConditionTypeCertificateSigningRequestApproved) || api.IsStatusConditionTrue(csr.Status.Conditions, api.ConditionTypeCertificateSigningRequestDenied) {
		return
	}

	api.SetStatusCondition(&csr.Status.Conditions, api.Condition{
		Type:    api.ConditionTypeCertificateSigningRequestApproved,
		Status:  api.ConditionStatusTrue,
		Reason:  "Approved",
		Message: "Auto-approved by enrollment signer",
	})
	api.RemoveStatusCondition(&csr.Status.Conditions, api.ConditionTypeCertificateSigningRequestFailed)

	if _, err := h.store.CertificateSigningRequest().UpdateStatus(ctx, orgId, csr); err != nil {
		h.log.WithError(err).Error("failed to set approval condition")
	}
}

func (h *ServiceHandler) signApprovedCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, csr *api.CertificateSigningRequest) {
	if csr.Status.Certificate != nil && len(*csr.Status.Certificate) > 0 {
		return
	}

	request, _, err := newSignRequestFromCertificateSigningRequest(csr)
	if err != nil {
		h.setCSRFailedCondition(ctx, orgId, csr, "SigningFailed", fmt.Sprintf("Failed to sign certificate: %v", err))
		return
	}

	certPEM, err := signer.SignAsPEM(ctx, h.ca, request)
	if err != nil {
		h.setCSRFailedCondition(ctx, orgId, csr, "SigningFailed", fmt.Sprintf("Failed to sign certificate: %v", err))
		return
	}

	csr.Status.Certificate = &certPEM
	if _, err := h.store.CertificateSigningRequest().UpdateStatus(ctx, orgId, csr); err != nil {
		h.log.WithError(err).Error("failed to set signed certificate")
	}
}

func (h *ServiceHandler) ListCertificateSigningRequests(ctx context.Context, orgId uuid.UUID, params api.ListCertificateSigningRequestsParams) (*api.CertificateSigningRequestList, api.Status) {
	listParams, status := prepareListParams(params.Continue, params.LabelSelector, params.FieldSelector, params.Limit)
	if status != api.StatusOK() {
		return nil, status
	}

	result, err := h.store.CertificateSigningRequest().List(ctx, orgId, *listParams)
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

func (h *ServiceHandler) verifyTPMCSRRequest(ctx context.Context, orgId uuid.UUID, csr *api.CertificateSigningRequest) error {
	if csr.Status == nil {
		csr.Status = &api.CertificateSigningRequestStatus{}
	}
	csrBytes, isTPM := tpm.ParseTCGCSRBytes(string(csr.Spec.Request))
	if !isTPM {
		return fmt.Errorf("parsing TCG CSR")
	}

	setTPMVerifiedFalse := func(messageTemplate string, args ...any) {
		api.SetStatusCondition(&csr.Status.Conditions, api.Condition{
			Message: fmt.Sprintf(messageTemplate, args...),
			Reason:  api.TPMVerificationFailedReason,
			Status:  api.ConditionStatusFalse,
			Type:    api.ConditionTypeCertificateSigningRequestTPMVerified,
		})
	}

	kind, owner, err := util.GetResourceOwner(csr.Metadata.Owner)
	if err != nil {
		setTPMVerifiedFalse("Failed to determine resource owner")
		return nil
	}
	if kind != api.DeviceKind {
		setTPMVerifiedFalse("The CSR's owner is not a %s", api.DeviceKind)
		return nil
	}
	// TODO this should be retrieved from the device rather than from the ER
	er, err := h.store.EnrollmentRequest().Get(ctx, orgId, owner)
	if err != nil {
		setTPMVerifiedFalse("Unable to find CSR's owner: %s/%s", orgId, owner)
		return nil
	}

	notTPMBasedMessage := fmt.Sprintf("The CSR's owner %s is not TPM based.", lo.FromPtr(csr.Metadata.Owner))
	if er.Status == nil || !api.IsStatusConditionTrue(er.Status.Conditions, api.ConditionTypeEnrollmentRequestTPMVerified) {
		setTPMVerifiedFalse(notTPMBasedMessage)
		return nil
	}

	erBytes, isTPM := tpm.ParseTCGCSRBytes(er.Spec.Csr)
	if !isTPM {
		setTPMVerifiedFalse(notTPMBasedMessage)
		return nil
	}

	parsed, err := tpm.ParseTCGCSR(erBytes)
	if err != nil {
		setTPMVerifiedFalse(notTPMBasedMessage)
		return nil
	}

	if err = tpm.VerifyTCGCSRSigningChain(csrBytes, parsed.CSRContents.Payload.AttestPub); err != nil {
		setTPMVerifiedFalse(err.Error())
		return nil
	}
	api.SetStatusCondition(&csr.Status.Conditions, api.Condition{
		Message: "TPM chain of trust verified",
		Reason:  "TPMVerificationSucceeded",
		Status:  api.ConditionStatusTrue,
		Type:    api.ConditionTypeCertificateSigningRequestTPMVerified,
	})

	return nil
}

func (h *ServiceHandler) CreateCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, csr api.CertificateSigningRequest) (*api.CertificateSigningRequest, api.Status) {
	// don't set fields that are managed by the service for external requests
	if !IsInternalRequest(ctx) {
		csr.Status = nil
		NilOutManagedObjectMetaProperties(&csr.Metadata)
	}

	// Support legacy shorthand "enrollment" by replacing it with the configured signer name
	if csr.Spec.SignerName == "enrollment" {
		csr.Spec.SignerName = h.ca.Cfg.DeviceEnrollmentSignerName
	}

	if errs := csr.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}

	if err := h.validateAllowedSignersForCSRService(&csr); err != nil {
		return nil, api.StatusBadRequest(err.Error())
	}

	request, isTPM, err := newSignRequestFromCertificateSigningRequest(&csr)
	if err != nil {
		return nil, api.StatusBadRequest(err.Error())
	}

	if err := signer.Verify(ctx, h.ca, request); err != nil {
		return nil, api.StatusBadRequest(err.Error())
	}
	if isTPM {
		if err = h.verifyTPMCSRRequest(ctx, orgId, &csr); err != nil {
			return nil, api.StatusBadRequest(err.Error())
		}
	}

	result, err := h.store.CertificateSigningRequest().Create(ctx, orgId, &csr, h.callbackCertificateSigningRequestUpdated)
	if err != nil {
		return nil, StoreErrorToApiStatus(err, true, api.CertificateSigningRequestKind, csr.Metadata.Name)
	}

	if result.Spec.SignerName == h.ca.Cfg.DeviceEnrollmentSignerName {
		h.autoApprove(ctx, orgId, result)
	}

	if api.IsStatusConditionTrue(result.Status.Conditions, api.ConditionTypeCertificateSigningRequestApproved) {
		h.signApprovedCertificateSigningRequest(ctx, orgId, result)
	}

	return result, api.StatusCreated()
}

func (h *ServiceHandler) DeleteCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, name string) api.Status {
	err := h.store.CertificateSigningRequest().Delete(ctx, orgId, name, h.callbackCertificateSigningRequestDeleted)
	return StoreErrorToApiStatus(err, false, api.CertificateSigningRequestKind, &name)
}

func (h *ServiceHandler) GetCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, name string) (*api.CertificateSigningRequest, api.Status) {
	result, err := h.store.CertificateSigningRequest().Get(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, api.CertificateSigningRequestKind, &name)
}

func (h *ServiceHandler) PatchCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, name string, patch api.PatchRequest) (*api.CertificateSigningRequest, api.Status) {
	currentObj, err := h.store.CertificateSigningRequest().Get(ctx, orgId, name)
	if err != nil {
		return nil, StoreErrorToApiStatus(err, false, api.CertificateSigningRequestKind, &name)
	}

	newObj := &api.CertificateSigningRequest{}
	err = ApplyJSONPatch(ctx, currentObj, newObj, patch, "/api/v1/certificatesigningrequests/"+name)
	if err != nil {
		return nil, api.StatusBadRequest(err.Error())
	}

	if errs := currentObj.ValidateUpdate(newObj); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}

	NilOutManagedObjectMetaProperties(&newObj.Metadata)
	newObj.Metadata.ResourceVersion = nil

	// Support legacy shorthand "enrollment" by replacing it with the configured signer name
	if newObj.Spec.SignerName == "enrollment" {
		newObj.Spec.SignerName = h.ca.Cfg.DeviceEnrollmentSignerName
	}

	if errs := newObj.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}

	if err := h.validateAllowedSignersForCSRService(newObj); err != nil {
		return nil, api.StatusBadRequest(err.Error())
	}

	request, isTPM, err := newSignRequestFromCertificateSigningRequest(newObj)
	if err != nil {
		return nil, api.StatusBadRequest(err.Error())
	}

	if err := signer.Verify(ctx, h.ca, request); err != nil {
		return nil, api.StatusBadRequest(err.Error())
	}
	if isTPM {
		if err = h.verifyTPMCSRRequest(ctx, orgId, newObj); err != nil {
			return nil, api.StatusBadRequest(err.Error())
		}
	}

	result, err := h.store.CertificateSigningRequest().Update(ctx, orgId, newObj, h.callbackCertificateSigningRequestUpdated)
	if err != nil {
		return nil, StoreErrorToApiStatus(err, false, api.CertificateSigningRequestKind, &name)
	}

	if result.Spec.SignerName == h.ca.Cfg.DeviceEnrollmentSignerName {
		h.autoApprove(ctx, orgId, result)
	}
	if api.IsStatusConditionTrue(result.Status.Conditions, api.ConditionTypeCertificateSigningRequestApproved) {
		h.signApprovedCertificateSigningRequest(ctx, orgId, result)
	}

	return result, api.StatusOK()
}

func (h *ServiceHandler) ReplaceCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, name string, csr api.CertificateSigningRequest) (*api.CertificateSigningRequest, api.Status) {
	// don't set fields that are managed by the service for external requests
	if !IsInternalRequest(ctx) {
		csr.Status = nil
		NilOutManagedObjectMetaProperties(&csr.Metadata)
	}

	if name != *csr.Metadata.Name {
		return nil, api.StatusBadRequest("resource name specified in metadata does not match name in path")
	}

	// Support legacy shorthand "enrollment" by replacing it with the configured signer name
	if csr.Spec.SignerName == "enrollment" {
		csr.Spec.SignerName = h.ca.Cfg.DeviceEnrollmentSignerName
	}

	if errs := csr.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}

	if err := h.validateAllowedSignersForCSRService(&csr); err != nil {
		return nil, api.StatusBadRequest(err.Error())
	}

	request, isTPM, err := newSignRequestFromCertificateSigningRequest(&csr)
	if err != nil {
		return nil, api.StatusBadRequest(err.Error())
	}

	if err := signer.Verify(ctx, h.ca, request); err != nil {
		return nil, api.StatusBadRequest(err.Error())
	}

	if isTPM {
		if err = h.verifyTPMCSRRequest(ctx, orgId, &csr); err != nil {
			return nil, api.StatusBadRequest(err.Error())
		}
	}

	result, created, err := h.store.CertificateSigningRequest().CreateOrUpdate(ctx, orgId, &csr, h.callbackCertificateSigningRequestUpdated)
	if err != nil {
		return nil, StoreErrorToApiStatus(err, created, api.CertificateSigningRequestKind, &name)
	}

	if result.Spec.SignerName == h.ca.Cfg.DeviceEnrollmentSignerName {
		h.autoApprove(ctx, orgId, result)
	}
	if api.IsStatusConditionTrue(result.Status.Conditions, api.ConditionTypeCertificateSigningRequestApproved) {
		h.signApprovedCertificateSigningRequest(ctx, orgId, result)
	}

	return result, StoreErrorToApiStatus(nil, created, api.CertificateSigningRequestKind, &name)
}

// NOTE: Approval currently also issues a certificate - this will change in the future based on policy
func (h *ServiceHandler) UpdateCertificateSigningRequestApproval(ctx context.Context, orgId uuid.UUID, name string, csr api.CertificateSigningRequest) (*api.CertificateSigningRequest, api.Status) {
	newCSR := &csr
	NilOutManagedObjectMetaProperties(&newCSR.Metadata)
	if errs := newCSR.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}
	if err := h.validateAllowedSignersForCSRService(&csr); err != nil {
		return nil, api.StatusBadRequest(err.Error())
	}
	if name != *newCSR.Metadata.Name {
		return nil, api.StatusBadRequest("resource name specified in metadata does not match name in path")
	}
	if newCSR.Status == nil {
		return nil, api.StatusBadRequest("status is required")
	}
	allowedConditionTypes := []api.ConditionType{
		api.ConditionTypeCertificateSigningRequestApproved,
		api.ConditionTypeCertificateSigningRequestDenied,
		api.ConditionTypeCertificateSigningRequestFailed,
		api.ConditionTypeCertificateSigningRequestTPMVerified,
	}
	// manual approving of TPMVerified false is allowed
	trueConditions := []api.ConditionType{
		api.ConditionTypeCertificateSigningRequestApproved,
		api.ConditionTypeCertificateSigningRequestDenied,
		api.ConditionTypeCertificateSigningRequestFailed,
	}
	exclusiveConditions := []api.ConditionType{api.ConditionTypeCertificateSigningRequestApproved, api.ConditionTypeCertificateSigningRequestDenied}
	errs := api.ValidateConditions(newCSR.Status.Conditions, allowedConditionTypes, trueConditions, exclusiveConditions)
	if len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}

	oldCSR, err := h.store.CertificateSigningRequest().Get(ctx, orgId, name)
	if err != nil {
		return nil, StoreErrorToApiStatus(err, false, api.CertificateSigningRequestKind, &name)
	}

	// do not approve a denied request, or recreate a cert for an already-approved request
	if api.IsStatusConditionTrue(oldCSR.Status.Conditions, api.ConditionTypeCertificateSigningRequestDenied) {
		return nil, api.StatusConflict("The request has already been denied")
	}
	if api.IsStatusConditionTrue(oldCSR.Status.Conditions, api.ConditionTypeCertificateSigningRequestApproved) && oldCSR.Status.Certificate != nil && len(*oldCSR.Status.Certificate) > 0 {
		return nil, api.StatusConflict("The request has already been approved and the certificate issued")
	}

	populateConditionTimestamps(newCSR, oldCSR)
	newConditions := newCSR.Status.Conditions

	// Updating the approval should only update the conditions.
	newCSR.Spec = oldCSR.Spec
	newCSR.Status = oldCSR.Status
	newCSR.Status.Conditions = newConditions

	result, err := h.store.CertificateSigningRequest().UpdateStatus(ctx, orgId, newCSR)
	if err != nil {
		return nil, StoreErrorToApiStatus(err, false, api.CertificateSigningRequestKind, &name)
	}

	if api.IsStatusConditionTrue(result.Status.Conditions, api.ConditionTypeCertificateSigningRequestApproved) {
		h.signApprovedCertificateSigningRequest(ctx, orgId, result)
	}

	return result, api.StatusOK()
}

func newSignRequestFromCertificateSigningRequest(csr *api.CertificateSigningRequest) (signer.SignRequest, bool, error) {
	var opts []signer.SignRequestOption
	csrData, isTPM, err := tpm.NormalizeEnrollmentCSR(string(csr.Spec.Request))
	if err != nil {
		return nil, isTPM, fmt.Errorf("normalizing CSR: %w", err)
	}

	if csr.Status != nil && csr.Status.Certificate != nil {
		opts = append(opts, signer.WithIssuedCertificateBytes(*csr.Status.Certificate))
	}

	if csr.Spec.ExpirationSeconds != nil {
		opts = append(opts, signer.WithExpirationSeconds(*csr.Spec.ExpirationSeconds))
	}

	if csr.Metadata.Name != nil {
		opts = append(opts, signer.WithResourceName(*csr.Metadata.Name))
	}

	signReq, err := signer.NewSignRequestFromBytes(csr.Spec.SignerName, csrData, opts...)
	return signReq, isTPM, err
}

// borrowed from https://github.com/kubernetes/kubernetes/blob/master/pkg/registry/certificates/certificates/strategy.go
func populateConditionTimestamps(newCSR, oldCSR *api.CertificateSigningRequest) {
	now := nowFunc()
	for i := range newCSR.Status.Conditions {
		// preserve existing lastTransitionTime if the condition with this type/status already exists,
		// otherwise set to now.
		if newCSR.Status.Conditions[i].LastTransitionTime.IsZero() {
			lastTransition := now
			for _, oldCondition := range oldCSR.Status.Conditions {
				if oldCondition.Type == newCSR.Status.Conditions[i].Type &&
					oldCondition.Status == newCSR.Status.Conditions[i].Status &&
					!oldCondition.LastTransitionTime.IsZero() {
					lastTransition = oldCondition.LastTransitionTime
					break
				}
			}
			newCSR.Status.Conditions[i].LastTransitionTime = lastTransition
		}
	}
}

func (h *ServiceHandler) validateAllowedSignersForCSRService(csr *api.CertificateSigningRequest) error {
	if csr.Spec.SignerName == h.ca.Cfg.DeviceManagementSignerName {
		return fmt.Errorf("signer name %q is not allowed in CertificateSigningRequest service; use the EnrollmentRequest API instead", csr.Spec.SignerName)
	}
	return nil
}

// callbackCertificateSigningRequestUpdated is the certificate signing request-specific callback that handles CSR events
func (h *ServiceHandler) callbackCertificateSigningRequestUpdated(ctx context.Context, resourceKind api.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	h.eventHandler.HandleCertificateSigningRequestUpdatedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
}

// callbackCertificateSigningRequestDeleted is the certificate signing request-specific callback that handles CSR deletion events
func (h *ServiceHandler) callbackCertificateSigningRequestDeleted(ctx context.Context, resourceKind api.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	h.eventHandler.HandleGenericResourceDeletedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
}

// setCSRFailedCondition sets the Failed condition on the provided CSR, persists the change, and logs any error during persistence.
func (h *ServiceHandler) setCSRFailedCondition(ctx context.Context, orgId uuid.UUID, csr *api.CertificateSigningRequest, reason, message string) {
	api.SetStatusCondition(&csr.Status.Conditions, api.Condition{
		Type:    api.ConditionTypeCertificateSigningRequestFailed,
		Status:  api.ConditionStatusTrue,
		Reason:  reason,
		Message: message,
	})

	if _, err := h.store.CertificateSigningRequest().UpdateStatus(ctx, orgId, csr); err != nil {
		h.log.WithError(err).Error("failed to set failure condition")
	}
}
