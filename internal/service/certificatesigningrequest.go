package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/google/uuid"
)

const DefaultEnrollmentCertExpirySeconds int32 = 60 * 60 * 24 * 7 // 7 days

// nowFunc allows overriding for unit tests
var nowFunc = time.Now

func (h *ServiceHandler) autoApprove(ctx context.Context, orgId uuid.UUID, csr *api.CertificateSigningRequest) {
	if api.IsStatusConditionTrue(csr.Status.Conditions, api.ConditionTypeCertificateSigningRequestApproved) {
		return
	}

	api.SetStatusCondition(&csr.Status.Conditions, api.Condition{
		Type:    api.ConditionTypeCertificateSigningRequestApproved,
		Status:  api.ConditionStatusTrue,
		Reason:  "Approved",
		Message: "Auto-approved by enrollment signer",
	})
	api.RemoveStatusCondition(&csr.Status.Conditions, api.ConditionTypeCertificateSigningRequestDenied)
	api.RemoveStatusCondition(&csr.Status.Conditions, api.ConditionTypeCertificateSigningRequestFailed)

	if _, err := h.store.CertificateSigningRequest().UpdateStatus(ctx, orgId, csr); err != nil {
		h.log.WithError(err).Error("failed to set approval condition")
	}
}

func (h *ServiceHandler) signApprovedCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, csr *api.CertificateSigningRequest) {
	if csr.Status.Certificate != nil && len(*csr.Status.Certificate) > 0 {
		return
	}

	signer := h.ca.GetSigner(csr.Spec.SignerName)
	if signer == nil {
		api.SetStatusCondition(&csr.Status.Conditions, api.Condition{
			Type:    api.ConditionTypeCertificateSigningRequestFailed,
			Status:  api.ConditionStatusTrue,
			Reason:  "SigningFailed",
			Message: fmt.Sprintf("No signer found for signer name %q", csr.Spec.SignerName),
		})
		if _, err := h.store.CertificateSigningRequest().UpdateStatus(ctx, orgId, csr); err != nil {
			h.log.WithError(err).Error("failed to set failure condition")
		}
		return
	}

	signedCert, err := signer.Sign(ctx, *csr)
	if err != nil {
		api.SetStatusCondition(&csr.Status.Conditions, api.Condition{
			Type:    api.ConditionTypeCertificateSigningRequestFailed,
			Status:  api.ConditionStatusTrue,
			Reason:  "SigningFailed",
			Message: fmt.Sprintf("Failed to sign certificate: %v", err),
		})
		if _, err := h.store.CertificateSigningRequest().UpdateStatus(ctx, orgId, csr); err != nil {
			h.log.WithError(err).Error("failed to set failure condition")
		}
		return
	}

	csr.Status.Certificate = &signedCert
	if _, err := h.store.CertificateSigningRequest().UpdateStatus(ctx, orgId, csr); err != nil {
		h.log.WithError(err).Error("failed to set signed certificate")
	}
}

func (h *ServiceHandler) ListCertificateSigningRequests(ctx context.Context, params api.ListCertificateSigningRequestsParams) (*api.CertificateSigningRequestList, api.Status) {
	orgId := store.NullOrgId

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

func (h *ServiceHandler) CreateCertificateSigningRequest(ctx context.Context, csr api.CertificateSigningRequest) (*api.CertificateSigningRequest, api.Status) {
	orgId := store.NullOrgId

	// don't set fields that are managed by the service for external requests
	if !IsInternalRequest(ctx) {
		csr.Status = nil
		NilOutManagedObjectMetaProperties(&csr.Metadata)
	}

	// Support legacy shorthand "enrollment" by replacing it with the configured signer name
	if csr.Spec.SignerName == "enrollment" {
		csr.Spec.SignerName = h.ca.Cfg.ClientBootstrapSignerName
	}

	if errs := csr.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}

	if err := h.validateAllowedSignersForCSRService(&csr); err != nil {
		return nil, api.StatusBadRequest(err.Error())
	}

	signer := h.ca.GetSigner(csr.Spec.SignerName)
	if signer == nil {
		return nil, api.StatusBadRequest(fmt.Sprintf("signer %q not found", csr.Spec.SignerName))
	}

	if err := signer.Verify(ctx, csr); err != nil {
		return nil, api.StatusBadRequest(err.Error())
	}

	result, err := h.store.CertificateSigningRequest().Create(ctx, orgId, &csr, h.eventCallback)
	if err != nil {
		return nil, StoreErrorToApiStatus(err, true, api.CertificateSigningRequestKind, csr.Metadata.Name)
	}

	if result.Spec.SignerName == h.ca.Cfg.ClientBootstrapSignerName {
		h.autoApprove(ctx, orgId, result)
	}

	if api.IsStatusConditionTrue(result.Status.Conditions, api.ConditionTypeCertificateSigningRequestApproved) {
		h.signApprovedCertificateSigningRequest(ctx, orgId, result)
	}

	return result, api.StatusCreated()
}

func (h *ServiceHandler) DeleteCertificateSigningRequest(ctx context.Context, name string) api.Status {
	orgId := store.NullOrgId

	err := h.store.CertificateSigningRequest().Delete(ctx, orgId, name, h.eventDeleteCallback)
	return StoreErrorToApiStatus(err, false, api.CertificateSigningRequestKind, &name)
}

func (h *ServiceHandler) GetCertificateSigningRequest(ctx context.Context, name string) (*api.CertificateSigningRequest, api.Status) {
	orgId := store.NullOrgId

	result, err := h.store.CertificateSigningRequest().Get(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, api.CertificateSigningRequestKind, &name)
}

func (h *ServiceHandler) PatchCertificateSigningRequest(ctx context.Context, name string, patch api.PatchRequest) (*api.CertificateSigningRequest, api.Status) {
	orgId := store.NullOrgId

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
		newObj.Spec.SignerName = h.ca.Cfg.ClientBootstrapSignerName
	}

	if errs := newObj.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}

	if err := h.validateAllowedSignersForCSRService(newObj); err != nil {
		return nil, api.StatusBadRequest(err.Error())
	}

	signer := h.ca.GetSigner(newObj.Spec.SignerName)
	if signer == nil {
		return nil, api.StatusBadRequest(fmt.Sprintf("signer %q not found", newObj.Spec.SignerName))
	}

	if err := signer.Verify(ctx, *newObj); err != nil {
		return nil, api.StatusBadRequest(err.Error())
	}

	result, err := h.store.CertificateSigningRequest().Update(ctx, orgId, newObj, h.eventDeleteCallback)
	if err != nil {
		return nil, StoreErrorToApiStatus(err, false, api.CertificateSigningRequestKind, &name)
	}

	if result.Spec.SignerName == h.ca.Cfg.ClientBootstrapSignerName {
		h.autoApprove(ctx, orgId, result)
	}
	if api.IsStatusConditionTrue(result.Status.Conditions, api.ConditionTypeCertificateSigningRequestApproved) {
		h.signApprovedCertificateSigningRequest(ctx, orgId, result)
	}

	return result, api.StatusOK()
}

func (h *ServiceHandler) ReplaceCertificateSigningRequest(ctx context.Context, name string, csr api.CertificateSigningRequest) (*api.CertificateSigningRequest, api.Status) {
	orgId := store.NullOrgId

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
		csr.Spec.SignerName = h.ca.Cfg.ClientBootstrapSignerName
	}

	if errs := csr.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}

	if err := h.validateAllowedSignersForCSRService(&csr); err != nil {
		return nil, api.StatusBadRequest(err.Error())
	}

	signer := h.ca.GetSigner(csr.Spec.SignerName)
	if signer == nil {
		return nil, api.StatusBadRequest(fmt.Sprintf("signer %q not found", csr.Spec.SignerName))
	}

	if err := signer.Verify(ctx, csr); err != nil {
		return nil, api.StatusBadRequest(err.Error())
	}

	result, created, err := h.store.CertificateSigningRequest().CreateOrUpdate(ctx, orgId, &csr, h.eventCallback)
	if err != nil {
		return nil, StoreErrorToApiStatus(err, created, api.CertificateSigningRequestKind, &name)
	}

	if result.Spec.SignerName == h.ca.Cfg.ClientBootstrapSignerName {
		h.autoApprove(ctx, orgId, result)
	}
	if api.IsStatusConditionTrue(result.Status.Conditions, api.ConditionTypeCertificateSigningRequestApproved) {
		h.signApprovedCertificateSigningRequest(ctx, orgId, result)
	}

	return result, StoreErrorToApiStatus(nil, created, api.CertificateSigningRequestKind, &name)
}

// NOTE: Approval currently also issues a certificate - this will change in the future based on policy
func (h *ServiceHandler) UpdateCertificateSigningRequestApproval(ctx context.Context, name string, csr api.CertificateSigningRequest) (*api.CertificateSigningRequest, api.Status) {
	orgId := store.NullOrgId

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
	allowedConditionTypes := []api.ConditionType{api.ConditionTypeCertificateSigningRequestApproved, api.ConditionTypeCertificateSigningRequestDenied, api.ConditionTypeCertificateSigningRequestFailed}
	trueConditions := allowedConditionTypes
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
	if csr.Spec.SignerName == h.ca.Cfg.DeviceEnrollmentSignerName {
		return fmt.Errorf("signer name %q is not allowed in CertificateSigningRequest service; use the EnrollmentRequest API instead", csr.Spec.SignerName)
	}
	return nil
}
