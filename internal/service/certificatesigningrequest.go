package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/go-openapi/swag"
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

func signApprovedCertificateSigningRequest(ca *crypto.CA, request api.CertificateSigningRequest) ([]byte, error) {

	csr, err := crypto.ParseCSR(request.Spec.Request)
	if err != nil {
		return nil, err
	}

	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("%w: %s", flterrors.ErrSignature, err)
	}

	// the CN will need the enrollment prefix applied;
	// if a CN is not specified in the CSR, generate a UUID to represent the future device
	u := csr.Subject.CommonName
	if u == "" {
		u = uuid.NewString()
	}
	csr.Subject.CommonName = crypto.BootstrapCNFromName(u)

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

// (DELETE /api/v1/certificatesigningrequests)
func (h *ServiceHandler) DeleteCertificateSigningRequests(ctx context.Context, request server.DeleteCertificateSigningRequestsRequestObject) (server.DeleteCertificateSigningRequestsResponseObject, error) {
	orgId := store.NullOrgId

	err := h.store.CertificateSigningRequest().DeleteAll(ctx, orgId)
	switch err {
	case nil:
		return server.DeleteCertificateSigningRequests200JSONResponse(api.StatusOK()), nil
	default:
		return nil, err
	}
}

// (GET /api/v1/certificatesigningrequests)
func (h *ServiceHandler) ListCertificateSigningRequests(ctx context.Context, request server.ListCertificateSigningRequestsRequestObject) (server.ListCertificateSigningRequestsResponseObject, error) {
	orgId := store.NullOrgId

	cont, err := store.ParseContinueString(request.Params.Continue)
	if err != nil {
		return server.ListCertificateSigningRequests400JSONResponse(api.StatusBadRequest(fmt.Sprintf("failed to parse continue parameter: %v", err))), nil
	}

	var fieldSelector *selector.FieldSelector
	if request.Params.FieldSelector != nil {
		if fieldSelector, err = selector.NewFieldSelector(*request.Params.FieldSelector); err != nil {
			return server.ListCertificateSigningRequests400JSONResponse(api.StatusBadRequest(fmt.Sprintf("failed to parse field selector: %v", err))), nil
		}
	}

	var labelSelector *selector.LabelSelector
	if request.Params.LabelSelector != nil {
		if labelSelector, err = selector.NewLabelSelector(*request.Params.LabelSelector); err != nil {
			return server.ListCertificateSigningRequests400JSONResponse(api.StatusBadRequest(fmt.Sprintf("failed to parse label selector: %v", err))), nil
		}
	}

	listParams := store.ListParams{
		Limit:         int(swag.Int32Value(request.Params.Limit)),
		Continue:      cont,
		FieldSelector: fieldSelector,
		LabelSelector: labelSelector,
	}
	if listParams.Limit == 0 {
		listParams.Limit = store.MaxRecordsPerListRequest
	}
	if listParams.Limit > store.MaxRecordsPerListRequest {
		return server.ListCertificateSigningRequests400JSONResponse(api.StatusBadRequest(fmt.Sprintf("limit cannot exceed %d", store.MaxRecordsPerListRequest))), nil
	}

	result, err := h.store.CertificateSigningRequest().List(ctx, orgId, listParams)
	if err == nil {
		return server.ListCertificateSigningRequests200JSONResponse(*result), nil
	}

	var se *selector.SelectorError

	switch {
	case selector.AsSelectorError(err, &se):
		return server.ListCertificateSigningRequests400JSONResponse(api.StatusBadRequest(se.Error())), nil
	default:
		return nil, err
	}
}

// (POST /api/v1/certificatesigningrequests)
func (h *ServiceHandler) CreateCertificateSigningRequest(ctx context.Context, request server.CreateCertificateSigningRequestRequestObject) (server.CreateCertificateSigningRequestResponseObject, error) {
	orgId := store.NullOrgId

	// don't set fields that are managed by the service
	request.Body.Status = nil
	common.NilOutManagedObjectMetaProperties(&request.Body.Metadata)

	if errs := request.Body.Validate(); len(errs) > 0 {
		return server.CreateCertificateSigningRequest400JSONResponse(api.StatusBadRequest(errors.Join(errs...).Error())), nil
	}

	result, err := h.store.CertificateSigningRequest().Create(ctx, orgId, request.Body)
	switch {
	case err == nil:
		break
	case errors.Is(err, flterrors.ErrResourceIsNil), errors.Is(err, flterrors.ErrIllegalResourceVersionFormat):
		return server.CreateCertificateSigningRequest400JSONResponse(api.StatusBadRequest(err.Error())), nil
	case errors.Is(err, flterrors.ErrDuplicateName):
		return server.CreateCertificateSigningRequest409JSONResponse(api.StatusResourceVersionConflict(err.Error())), nil
	default:
		return nil, err
	}

	if result.Spec.SignerName == "enrollment" {
		h.autoApprove(ctx, orgId, result)
	}
	if api.IsStatusConditionTrue(result.Status.Conditions, api.CertificateSigningRequestApproved) {
		h.signApprovedCertificateSigningRequest(ctx, orgId, result)
	}

	return server.CreateCertificateSigningRequest201JSONResponse(*result), nil
}

// (DELETE /api/v1/certificatesigningrequests/{name})
func (h *ServiceHandler) DeleteCertificateSigningRequest(ctx context.Context, request server.DeleteCertificateSigningRequestRequestObject) (server.DeleteCertificateSigningRequestResponseObject, error) {
	orgId := store.NullOrgId

	err := h.store.CertificateSigningRequest().Delete(ctx, orgId, request.Name)
	switch {
	case err == nil:
		return server.DeleteCertificateSigningRequest200JSONResponse{}, nil
	case errors.Is(err, flterrors.ErrResourceNotFound):
		return server.DeleteCertificateSigningRequest404JSONResponse(api.StatusResourceNotFound("CertificateSigningRequest", request.Name)), nil
	default:
		return nil, err
	}
}

// (GET /api/v1/certificatesigningrequests/{name})
func (h *ServiceHandler) ReadCertificateSigningRequest(ctx context.Context, request server.ReadCertificateSigningRequestRequestObject) (server.ReadCertificateSigningRequestResponseObject, error) {
	orgId := store.NullOrgId

	result, err := h.store.CertificateSigningRequest().Get(ctx, orgId, request.Name)
	switch {
	case err == nil:
		return server.ReadCertificateSigningRequest200JSONResponse(*result), nil
	case errors.Is(err, flterrors.ErrResourceNotFound):
		return server.ReadCertificateSigningRequest404JSONResponse(api.StatusResourceNotFound("CertificateSigningRequest", request.Name)), nil
	default:
		return nil, err
	}
}

// (PATCH /api/v1/certificatesigningrequests/{name})
func (h *ServiceHandler) PatchCertificateSigningRequest(ctx context.Context, request server.PatchCertificateSigningRequestRequestObject) (server.PatchCertificateSigningRequestResponseObject, error) {
	orgId := store.NullOrgId

	currentObj, err := h.store.CertificateSigningRequest().Get(ctx, orgId, request.Name)
	if err != nil {
		switch {
		case errors.Is(err, flterrors.ErrResourceIsNil), errors.Is(err, flterrors.ErrResourceNameIsNil):
			return server.PatchCertificateSigningRequest400JSONResponse(api.StatusBadRequest(err.Error())), nil
		case errors.Is(err, flterrors.ErrResourceNotFound):
			return server.PatchCertificateSigningRequest404JSONResponse(api.StatusResourceNotFound("CertificateSigningRequest", request.Name)), nil
		default:
			return nil, err
		}
	}

	newObj := &api.CertificateSigningRequest{}
	err = ApplyJSONPatch(ctx, currentObj, newObj, *request.Body, "/api/v1/certificatesigningrequests/"+request.Name)
	if err != nil {
		return server.PatchCertificateSigningRequest400JSONResponse(api.StatusBadRequest(err.Error())), nil
	}

	if newObj.Metadata.Name == nil || *currentObj.Metadata.Name != *newObj.Metadata.Name {
		return server.PatchCertificateSigningRequest400JSONResponse(api.StatusBadRequest("metadata.name is immutable")), nil
	}
	if currentObj.ApiVersion != newObj.ApiVersion {
		return server.PatchCertificateSigningRequest400JSONResponse(api.StatusBadRequest("apiVersion is immutable")), nil
	}
	if currentObj.Kind != newObj.Kind {
		return server.PatchCertificateSigningRequest400JSONResponse(api.StatusBadRequest("kind is immutable")), nil
	}
	if !reflect.DeepEqual(currentObj.Status, newObj.Status) {
		return server.PatchCertificateSigningRequest400JSONResponse(api.StatusBadRequest("status is immutable")), nil
	}

	common.NilOutManagedObjectMetaProperties(&newObj.Metadata)
	newObj.Metadata.ResourceVersion = nil

	result, err := h.store.CertificateSigningRequest().Update(ctx, orgId, newObj)
	switch {
	case err == nil:
		break
	case errors.Is(err, flterrors.ErrResourceIsNil), errors.Is(err, flterrors.ErrResourceNameIsNil):
		return server.PatchCertificateSigningRequest400JSONResponse(api.StatusBadRequest(err.Error())), nil
	case errors.Is(err, flterrors.ErrResourceNotFound):
		return server.PatchCertificateSigningRequest404JSONResponse(api.StatusResourceNotFound("CertificateSigningRequest", request.Name)), nil
	case errors.Is(err, flterrors.ErrNoRowsUpdated), errors.Is(err, flterrors.ErrResourceVersionConflict):
		return server.PatchCertificateSigningRequest409JSONResponse(api.StatusResourceVersionConflict(err.Error())), nil
	default:
		return nil, err
	}

	if result.Spec.SignerName == "enrollment" {
		h.autoApprove(ctx, orgId, result)
	}
	if api.IsStatusConditionTrue(result.Status.Conditions, api.CertificateSigningRequestApproved) {
		h.signApprovedCertificateSigningRequest(ctx, orgId, result)
	}

	return server.PatchCertificateSigningRequest200JSONResponse(*result), nil
}

// (PUT /api/v1/certificatesigningrequests/{name})
func (h *ServiceHandler) ReplaceCertificateSigningRequest(ctx context.Context, request server.ReplaceCertificateSigningRequestRequestObject) (server.ReplaceCertificateSigningRequestResponseObject, error) {
	orgId := store.NullOrgId

	// don't overwrite fields that are managed by the service
	request.Body.Status = nil
	common.NilOutManagedObjectMetaProperties(&request.Body.Metadata)

	if errs := request.Body.Validate(); len(errs) > 0 {
		return server.ReplaceCertificateSigningRequest400JSONResponse(api.StatusBadRequest(errors.Join(errs...).Error())), nil
	}
	if request.Name != *request.Body.Metadata.Name {
		return server.ReplaceCertificateSigningRequest400JSONResponse(api.StatusBadRequest("resource name specified in metadata does not match name in path")), nil
	}

	result, created, err := h.store.CertificateSigningRequest().CreateOrUpdate(ctx, orgId, request.Body)
	switch {
	case err == nil:
		break
	case errors.Is(err, flterrors.ErrResourceIsNil):
		return server.ReplaceCertificateSigningRequest400JSONResponse(api.StatusBadRequest(err.Error())), nil
	case errors.Is(err, flterrors.ErrResourceNameIsNil):
		return server.ReplaceCertificateSigningRequest400JSONResponse(api.StatusBadRequest(err.Error())), nil
	case errors.Is(err, flterrors.ErrResourceNotFound):
		return server.ReplaceCertificateSigningRequest404JSONResponse(api.StatusResourceNotFound("CertificateSigningRequest", request.Name)), nil
	case errors.Is(err, flterrors.ErrNoRowsUpdated), errors.Is(err, flterrors.ErrResourceVersionConflict):
		return server.ReplaceCertificateSigningRequest409JSONResponse(api.StatusResourceVersionConflict(err.Error())), nil
	default:
		return nil, err
	}

	if result.Spec.SignerName == "enrollment" {
		h.autoApprove(ctx, orgId, result)
	}
	if api.IsStatusConditionTrue(result.Status.Conditions, api.CertificateSigningRequestApproved) {
		h.signApprovedCertificateSigningRequest(ctx, orgId, result)
	}

	if created {
		return server.ReplaceCertificateSigningRequest201JSONResponse(*result), nil
	} else {
		return server.ReplaceCertificateSigningRequest200JSONResponse(*result), nil
	}
}

// (PUT /api/v1/certificatesigningrequests/{name}/approval)
// NOTE: Approval currently also issues a certificate - this will change in the future based on policy
func (h *ServiceHandler) UpdateCertificateSigningRequestApproval(ctx context.Context, request server.UpdateCertificateSigningRequestApprovalRequestObject) (server.UpdateCertificateSigningRequestApprovalResponseObject, error) {
	orgId := store.NullOrgId

	newCSR := request.Body
	common.NilOutManagedObjectMetaProperties(&newCSR.Metadata)
	if errs := newCSR.Validate(); len(errs) > 0 {
		return server.UpdateCertificateSigningRequestApproval400JSONResponse(api.StatusBadRequest(errors.Join(errs...).Error())), nil
	}
	if request.Name != *newCSR.Metadata.Name {
		return server.UpdateCertificateSigningRequestApproval400JSONResponse(api.StatusBadRequest("resource name specified in metadata does not match name in path")), nil
	}
	if newCSR.Status == nil {
		return server.UpdateCertificateSigningRequestApproval400JSONResponse(api.StatusBadRequest("status is required")), nil
	}
	allowedConditionTypes := []api.ConditionType{api.CertificateSigningRequestApproved, api.CertificateSigningRequestDenied, api.CertificateSigningRequestFailed}
	trueConditions := allowedConditionTypes
	exclusiveConditions := []api.ConditionType{api.CertificateSigningRequestApproved, api.CertificateSigningRequestDenied}
	errs := api.ValidateConditions(newCSR.Status.Conditions, allowedConditionTypes, trueConditions, exclusiveConditions)
	if len(errs) > 0 {
		return server.UpdateCertificateSigningRequestApproval400JSONResponse(api.StatusBadRequest(errors.Join(errs...).Error())), nil
	}

	oldCSR, err := h.store.CertificateSigningRequest().Get(ctx, orgId, request.Name)
	switch {
	case err == nil:
		break
	case errors.Is(err, flterrors.ErrResourceIsNil), errors.Is(err, flterrors.ErrResourceNameIsNil):
		return server.UpdateCertificateSigningRequestApproval400JSONResponse(api.StatusBadRequest(err.Error())), nil
	case errors.Is(err, flterrors.ErrResourceNotFound):
		return server.UpdateCertificateSigningRequestApproval404JSONResponse(api.StatusResourceNotFound("CertificateSigningRequest", request.Name)), nil
	default:
		return nil, err
	}

	// do not approve a denied request, or recreate a cert for an already-approved request
	if api.IsStatusConditionTrue(oldCSR.Status.Conditions, api.CertificateSigningRequestDenied) {
		return server.UpdateCertificateSigningRequestApproval409JSONResponse(api.StatusConflict("The request has already been denied")), nil
	}
	if api.IsStatusConditionTrue(oldCSR.Status.Conditions, api.CertificateSigningRequestApproved) && oldCSR.Status.Certificate != nil && len(*oldCSR.Status.Certificate) > 0 {
		return server.UpdateCertificateSigningRequestApproval409JSONResponse(api.StatusConflict("The request has already been approved and the certificate issued")), nil
	}

	populateConditionTimestamps(newCSR, oldCSR)
	newConditions := newCSR.Status.Conditions

	// Updating the approval should only update the conditions.
	newCSR.Spec = oldCSR.Spec
	newCSR.Status = oldCSR.Status
	newCSR.Status.Conditions = newConditions

	result, err := h.store.CertificateSigningRequest().UpdateStatus(ctx, orgId, newCSR)
	switch {
	case err == nil:
		break
	case errors.Is(err, flterrors.ErrResourceNotFound):
		return server.UpdateCertificateSigningRequestApproval404JSONResponse(api.StatusResourceNotFound("CertificateSigningRequest", request.Name)), nil
	case errors.Is(err, flterrors.ErrNoRowsUpdated), errors.Is(err, flterrors.ErrResourceVersionConflict):
		return server.UpdateCertificateSigningRequestApproval409JSONResponse(api.StatusResourceVersionConflict(err.Error())), nil
	default:
		return nil, err
	}

	if api.IsStatusConditionTrue(result.Status.Conditions, api.CertificateSigningRequestApproved) {
		h.signApprovedCertificateSigningRequest(ctx, orgId, result)
	}

	return server.UpdateCertificateSigningRequestApproval200JSONResponse(*result), nil
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
