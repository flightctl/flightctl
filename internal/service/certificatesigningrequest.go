package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/google/uuid"
)

const DefaultEnrollmentCertExpirySeconds int32 = 60 * 60 * 24 * 7 // 7 days

// nowFunc allows overriding for unit tests
var nowFunc = time.Now

func (h *ServiceHandler) autoApprove(ctx context.Context, orgId uuid.UUID, csr *api.CertificateSigningRequest) {
	if api.IsStatusConditionTrue(csr.Status.Conditions, api.CertificateSigningRequestApproved) {
		return
	}

	api.SetStatusCondition(&csr.Status.Conditions, api.Condition{
		Type:    api.CertificateSigningRequestApproved,
		Status:  api.ConditionStatusTrue,
		Reason:  "Approved",
		Message: "Auto-approved by enrollment signer",
	})
	api.RemoveStatusCondition(&csr.Status.Conditions, api.CertificateSigningRequestDenied)
	api.RemoveStatusCondition(&csr.Status.Conditions, api.CertificateSigningRequestFailed)

	if _, err := h.store.CertificateSigningRequest().UpdateStatus(ctx, orgId, csr); err != nil {
		h.log.WithError(err).Error("failed to set approval condition")
	}
}

func (h *ServiceHandler) signApprovedCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, csr *api.CertificateSigningRequest) {
	if csr.Status.Certificate != nil && len(*csr.Status.Certificate) > 0 {
		return
	}

	signedCert, err := signApprovedCertificateSigningRequest(h.ca, *csr)
	if err != nil {
		api.SetStatusCondition(&csr.Status.Conditions, api.Condition{
			Type:    api.CertificateSigningRequestFailed,
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

func signApprovedCertificateSigningRequest(ca *crypto.CAClient, request api.CertificateSigningRequest) ([]byte, error) {

	csr, err := crypto.ParseCSR(request.Spec.Request)
	if err != nil {
		return nil, err
	}

	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("%w: %s", flterrors.ErrSignature, err)
	}

	// the CN will need the enrollment prefix applied;
	// if the certificate is being renewed, the name will have an existing prefix.
	// we do not touch in this case.

	u := csr.Subject.CommonName

	// Once we move all prefixes/name formation to the client this can become a simple
	// comparison of u and *request.Metadata.Name

	if ca.BootstrapCNFromName(u) != ca.BootstrapCNFromName(*request.Metadata.Name) {
		return nil, fmt.Errorf("%w - CN %s Metadata %s mismatch", flterrors.ErrSignCert, u, *request.Metadata.Name)
	}

	csr.Subject.CommonName = ca.BootstrapCNFromName(u)

	expiry := DefaultEnrollmentCertExpirySeconds
	if request.Spec.ExpirationSeconds != nil {
		expiry = *request.Spec.ExpirationSeconds
	}

	certData, err := ca.IssueRequestedClientCertificate(csr, int(expiry))
	if err != nil {
		return nil, err
	}

	return certData, nil
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

func (h *ServiceHandler) verifyCSRParameters(ctx context.Context, csr api.CertificateSigningRequest) error {

	// Crypto validation
	cn, ok := ctx.Value(middleware.TLSCommonNameContextKey).(string)

	// Note - if auth is disabled and there is no mTLS handshake we get ok == False.
	// We cannot check anything in that case.

	if ok {
		if csr.Spec.SignerName != h.ca.Cfg.ClientBootstrapSignerName {
			if csr.Metadata.Name == nil {
				return errors.New("invalid csr record - no name in metadata")
			}
			if cn != h.ca.BootstrapCNFromName(*csr.Metadata.Name) {
				return errors.New("denied attempt to renew other entity certificate")
			}
		}
	}
	return nil
}

func (h *ServiceHandler) CreateCertificateSigningRequest(ctx context.Context, csr api.CertificateSigningRequest) (*api.CertificateSigningRequest, api.Status) {
	orgId := store.NullOrgId

	// don't set fields that are managed by the service
	csr.Status = nil
	NilOutManagedObjectMetaProperties(&csr.Metadata)

	if errs := csr.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}

	err := h.verifyCSRParameters(ctx, csr)
	if err != nil {
		return nil, api.StatusUnauthorized(err.Error())
	}

	result, err := h.store.CertificateSigningRequest().Create(ctx, orgId, &csr)
	if err != nil {
		status := StoreErrorToApiStatus(err, true, api.CertificateSigningRequestKind, csr.Metadata.Name)
		h.CreateEvent(ctx, GetResourceCreatedOrUpdatedEvent(ctx, true, api.CertificateSigningRequestKind, *csr.Metadata.Name, status, nil))
		return nil, status
	}

	if result.Spec.SignerName == h.ca.Cfg.ClientBootstrapSignerName {
		h.autoApprove(ctx, orgId, result)
	}

	if api.IsStatusConditionTrue(result.Status.Conditions, api.CertificateSigningRequestApproved) {
		h.signApprovedCertificateSigningRequest(ctx, orgId, result)
	}

	h.CreateEvent(ctx, GetResourceCreatedOrUpdatedEvent(ctx, true, api.CertificateSigningRequestKind, *csr.Metadata.Name, api.StatusCreated(), nil))
	return result, api.StatusCreated()
}

func (h *ServiceHandler) DeleteCertificateSigningRequest(ctx context.Context, name string) api.Status {
	orgId := store.NullOrgId

	err := h.store.CertificateSigningRequest().Delete(ctx, orgId, name)
	status := StoreErrorToApiStatus(err, false, api.CertificateSigningRequestKind, &name)
	h.CreateEvent(ctx, GetResourceDeletedEvent(ctx, api.CertificateSigningRequestKind, name, status))
	return status
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

	result, updatedDesc, err := h.store.CertificateSigningRequest().Update(ctx, orgId, newObj)
	if err != nil {
		status := StoreErrorToApiStatus(err, false, api.CertificateSigningRequestKind, &name)
		h.CreateEvent(ctx, GetResourceCreatedOrUpdatedEvent(ctx, false, api.CertificateSigningRequestKind, name, status, &updatedDesc))
		return nil, status
	}

	if result.Spec.SignerName == h.ca.Cfg.ClientBootstrapSignerName {
		h.autoApprove(ctx, orgId, result)
	}
	if api.IsStatusConditionTrue(result.Status.Conditions, api.CertificateSigningRequestApproved) {
		h.signApprovedCertificateSigningRequest(ctx, orgId, result)
	}

	h.CreateEvent(ctx, GetResourceCreatedOrUpdatedEvent(ctx, false, api.CertificateSigningRequestKind, name, api.StatusOK(), &updatedDesc))
	return result, api.StatusOK()
}

func (h *ServiceHandler) ReplaceCertificateSigningRequest(ctx context.Context, name string, csr api.CertificateSigningRequest) (*api.CertificateSigningRequest, api.Status) {
	orgId := store.NullOrgId

	// don't overwrite fields that are managed by the service
	csr.Status = nil
	NilOutManagedObjectMetaProperties(&csr.Metadata)

	if errs := csr.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}
	if name != *csr.Metadata.Name {
		return nil, api.StatusBadRequest("resource name specified in metadata does not match name in path")
	}

	err := h.verifyCSRParameters(ctx, csr)
	if err != nil {
		return nil, api.StatusUnauthorized(err.Error())
	}

	result, created, updatedDesc, err := h.store.CertificateSigningRequest().CreateOrUpdate(ctx, orgId, &csr)
	if err != nil {
		status := StoreErrorToApiStatus(err, created, api.CertificateSigningRequestKind, &name)
		h.CreateEvent(ctx, GetResourceCreatedOrUpdatedEvent(ctx, created, api.CertificateSigningRequestKind, name, status, &updatedDesc))
		return nil, status
	}

	if result.Spec.SignerName == h.ca.Cfg.ClientBootstrapSignerName {
		h.autoApprove(ctx, orgId, result)
	}
	if api.IsStatusConditionTrue(result.Status.Conditions, api.CertificateSigningRequestApproved) {
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
	if name != *newCSR.Metadata.Name {
		return nil, api.StatusBadRequest("resource name specified in metadata does not match name in path")
	}
	if newCSR.Status == nil {
		return nil, api.StatusBadRequest("status is required")
	}
	allowedConditionTypes := []api.ConditionType{api.CertificateSigningRequestApproved, api.CertificateSigningRequestDenied, api.CertificateSigningRequestFailed}
	trueConditions := allowedConditionTypes
	exclusiveConditions := []api.ConditionType{api.CertificateSigningRequestApproved, api.CertificateSigningRequestDenied}
	errs := api.ValidateConditions(newCSR.Status.Conditions, allowedConditionTypes, trueConditions, exclusiveConditions)
	if len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}

	oldCSR, err := h.store.CertificateSigningRequest().Get(ctx, orgId, name)
	if err != nil {
		return nil, StoreErrorToApiStatus(err, false, api.CertificateSigningRequestKind, &name)
	}

	// do not approve a denied request, or recreate a cert for an already-approved request
	if api.IsStatusConditionTrue(oldCSR.Status.Conditions, api.CertificateSigningRequestDenied) {
		return nil, api.StatusConflict("The request has already been denied")
	}
	if api.IsStatusConditionTrue(oldCSR.Status.Conditions, api.CertificateSigningRequestApproved) && oldCSR.Status.Certificate != nil && len(*oldCSR.Status.Certificate) > 0 {
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

	if api.IsStatusConditionTrue(result.Status.Conditions, api.CertificateSigningRequestApproved) {
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
