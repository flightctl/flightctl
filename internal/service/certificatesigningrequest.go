package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/auth"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/go-openapi/swag"
	"github.com/google/uuid"
)

const DefaultEnrollmentCertExpirySeconds int32 = 60 * 60 * 24 * 7 // 7 days

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
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "certificatesigningrequests", "deletecollection")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.DeleteCertificateSigningRequests503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.DeleteCertificateSigningRequests403JSONResponse{Message: Forbidden}, nil
	}
	orgId := store.NullOrgId

	err = h.store.CertificateSigningRequest().DeleteAll(ctx, orgId)
	switch err {
	case nil:
		return server.DeleteCertificateSigningRequests200JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (GET /api/v1/certificatesigningrequests)
func (h *ServiceHandler) ListCertificateSigningRequests(ctx context.Context, request server.ListCertificateSigningRequestsRequestObject) (server.ListCertificateSigningRequestsResponseObject, error) {
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "certificatesigningrequests", "list")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.ListCertificateSigningRequests503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.ListCertificateSigningRequests403JSONResponse{Message: Forbidden}, nil
	}
	orgId := store.NullOrgId

	cont, err := store.ParseContinueString(request.Params.Continue)
	if err != nil {
		return server.ListCertificateSigningRequests400JSONResponse{Message: fmt.Sprintf("failed to parse continue parameter: %v", err)}, nil
	}

	var fieldSelector *selector.FieldSelector
	if request.Params.FieldSelector != nil {
		if fieldSelector, err = selector.NewFieldSelector(*request.Params.FieldSelector); err != nil {
			return server.ListCertificateSigningRequests400JSONResponse{Message: fmt.Sprintf("failed to parse field selector: %v", err)}, nil
		}
	}

	var labelSelector *selector.LabelSelector
	if request.Params.LabelSelector != nil {
		if labelSelector, err = selector.NewLabelSelector(*request.Params.LabelSelector); err != nil {
			return server.ListCertificateSigningRequests400JSONResponse{Message: fmt.Sprintf("failed to parse label selector: %v", err)}, nil
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
		return server.ListCertificateSigningRequests400JSONResponse{Message: fmt.Sprintf("limit cannot exceed %d", store.MaxRecordsPerListRequest)}, nil
	}

	result, err := h.store.CertificateSigningRequest().List(ctx, orgId, listParams)
	if err == nil {
		return server.ListCertificateSigningRequests200JSONResponse(*result), nil
	}

	var se *selector.SelectorError

	switch {
	case selector.AsSelectorError(err, &se):
		return server.ListCertificateSigningRequests400JSONResponse{Message: se.Error()}, nil
	default:
		return nil, err
	}
}

// (POST /api/v1/certificatesigningrequests)
func (h *ServiceHandler) CreateCertificateSigningRequest(ctx context.Context, request server.CreateCertificateSigningRequestRequestObject) (server.CreateCertificateSigningRequestResponseObject, error) {
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "certificatesigningrequests", "create")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.CreateCertificateSigningRequest503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.CreateCertificateSigningRequest403JSONResponse{Message: Forbidden}, nil
	}
	orgId := store.NullOrgId

	// don't set fields that are managed by the service
	request.Body.Status = nil
	common.NilOutManagedObjectMetaProperties(&request.Body.Metadata)

	if errs := request.Body.Validate(); len(errs) > 0 {
		return server.CreateCertificateSigningRequest400JSONResponse{Message: errors.Join(errs...).Error()}, nil
	}

	result, err := h.store.CertificateSigningRequest().Create(ctx, orgId, request.Body)
	switch err {
	case nil:
		return server.CreateCertificateSigningRequest201JSONResponse(*result), nil
	case flterrors.ErrResourceIsNil:
		return server.CreateCertificateSigningRequest400JSONResponse{Message: err.Error()}, nil

	default:
		return nil, err
	}
}

// (DELETE /api/v1/certificatesigningrequests/{name})
func (h *ServiceHandler) DeleteCertificateSigningRequest(ctx context.Context, request server.DeleteCertificateSigningRequestRequestObject) (server.DeleteCertificateSigningRequestResponseObject, error) {
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "certificatesigningrequests", "delete")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.DeleteCertificateSigningRequest503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.DeleteCertificateSigningRequest403JSONResponse{Message: Forbidden}, nil
	}
	orgId := store.NullOrgId

	err = h.store.CertificateSigningRequest().Delete(ctx, orgId, request.Name)
	switch err {
	case nil:
		return server.DeleteCertificateSigningRequest200JSONResponse{}, nil
	case flterrors.ErrResourceNotFound:
		return server.DeleteCertificateSigningRequest404JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (GET /api/v1/certificatesigningrequests/{name})
func (h *ServiceHandler) ReadCertificateSigningRequest(ctx context.Context, request server.ReadCertificateSigningRequestRequestObject) (server.ReadCertificateSigningRequestResponseObject, error) {
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "certificatesigningrequests", "get")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.ReadCertificateSigningRequest503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.ReadCertificateSigningRequest403JSONResponse{Message: Forbidden}, nil
	}
	orgId := store.NullOrgId

	result, err := h.store.CertificateSigningRequest().Get(ctx, orgId, request.Name)
	switch err {
	case nil:
		return server.ReadCertificateSigningRequest200JSONResponse(*result), nil
	case flterrors.ErrResourceNotFound:
		return server.ReadCertificateSigningRequest404JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (PATCH /api/v1/certificatesigningrequests/{name})
func (h *ServiceHandler) PatchCertificateSigningRequest(ctx context.Context, request server.PatchCertificateSigningRequestRequestObject) (server.PatchCertificateSigningRequestResponseObject, error) {
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "certificatesigningrequests", "patch")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.PatchCertificateSigningRequest503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.PatchCertificateSigningRequest403JSONResponse{Message: Forbidden}, nil
	}
	orgId := store.NullOrgId

	currentObj, err := h.store.CertificateSigningRequest().Get(ctx, orgId, request.Name)
	if err != nil {
		switch err {
		case flterrors.ErrResourceIsNil, flterrors.ErrResourceNameIsNil:
			return server.PatchCertificateSigningRequest400JSONResponse{Message: err.Error()}, nil
		case flterrors.ErrResourceNotFound:
			return server.PatchCertificateSigningRequest404JSONResponse{}, nil
		default:
			return nil, err
		}
	}

	newObj := &api.CertificateSigningRequest{}
	err = ApplyJSONPatch(ctx, currentObj, newObj, *request.Body, "/api/v1/certificatesigningrequests/"+request.Name)
	if err != nil {
		return server.PatchCertificateSigningRequest400JSONResponse{Message: err.Error()}, nil
	}

	if newObj.Metadata.Name == nil || *currentObj.Metadata.Name != *newObj.Metadata.Name {
		return server.PatchCertificateSigningRequest400JSONResponse{Message: "metadata.name is immutable"}, nil
	}
	if currentObj.ApiVersion != newObj.ApiVersion {
		return server.PatchCertificateSigningRequest400JSONResponse{Message: "apiVersion is immutable"}, nil
	}
	if currentObj.Kind != newObj.Kind {
		return server.PatchCertificateSigningRequest400JSONResponse{Message: "kind is immutable"}, nil
	}
	if !reflect.DeepEqual(currentObj.Status, newObj.Status) {
		return server.PatchCertificateSigningRequest400JSONResponse{Message: "status is immutable"}, nil
	}

	common.NilOutManagedObjectMetaProperties(&newObj.Metadata)
	newObj.Metadata.ResourceVersion = nil

	result, err := h.store.CertificateSigningRequest().Update(ctx, orgId, newObj)

	switch err {
	case nil:
		return server.PatchCertificateSigningRequest200JSONResponse(*result), nil
	case flterrors.ErrResourceIsNil, flterrors.ErrResourceNameIsNil:
		return server.PatchCertificateSigningRequest400JSONResponse{Message: err.Error()}, nil
	case flterrors.ErrResourceNotFound:
		return server.PatchCertificateSigningRequest404JSONResponse{}, nil
	case flterrors.ErrNoRowsUpdated, flterrors.ErrResourceVersionConflict:
		return server.PatchCertificateSigningRequest409JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (PUT /api/v1/certificatesigningrequests/{name})
func (h *ServiceHandler) ReplaceCertificateSigningRequest(ctx context.Context, request server.ReplaceCertificateSigningRequestRequestObject) (server.ReplaceCertificateSigningRequestResponseObject, error) {
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "certificatesigningrequests", "update")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.ReplaceCertificateSigningRequest503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.ReplaceCertificateSigningRequest403JSONResponse{Message: Forbidden}, nil
	}
	orgId := store.NullOrgId

	// don't overwrite fields that are managed by the service
	request.Body.Status = nil
	common.NilOutManagedObjectMetaProperties(&request.Body.Metadata)

	if errs := request.Body.Validate(); len(errs) > 0 {
		return server.ReplaceCertificateSigningRequest400JSONResponse{Message: errors.Join(errs...).Error()}, nil
	}
	if request.Name != *request.Body.Metadata.Name {
		return server.ReplaceCertificateSigningRequest400JSONResponse{Message: "resource name specified in metadata does not match name in path"}, nil
	}

	result, created, err := h.store.CertificateSigningRequest().CreateOrUpdate(ctx, orgId, request.Body)
	switch err {
	case nil:
		// only attempt to auto-approve newly created CSR requests for an enrollment cert
		if created && request.Body.Spec.SignerName == "enrollment" {
			approveReq := server.ApproveCertificateSigningRequestRequestObject{
				Name: *request.Body.Metadata.Name,
			}
			approveResp, _ := h.ApproveCertificateSigningRequest(ctx, approveReq)
			_, ok := approveResp.(server.ApproveCertificateSigningRequest200JSONResponse)
			if !ok {
				c := []api.Condition{{
					Message: "CSR could not be auto-approved",
					Reason:  "CSR could not be auto-approved",
					Type:    "Failed"}}
				err := h.store.CertificateSigningRequest().UpdateConditions(ctx, orgId, request.Name, c)
				if err != nil {
					return nil, fmt.Errorf("status condition for %s could not be set to 'failed' upon failure to auto-approve: %w\nauto-approve failure: %s", request.Name, err, approveResp)
				}
				return nil, fmt.Errorf("enrollment CSR for %s could not be auto-approved: %s", request.Name, approveResp)
			}
		}
		if created {
			return server.ReplaceCertificateSigningRequest201JSONResponse(*result), nil
		} else {
			return server.ReplaceCertificateSigningRequest200JSONResponse(*result), nil
		}
	case flterrors.ErrResourceIsNil:
		return server.ReplaceCertificateSigningRequest400JSONResponse{Message: err.Error()}, nil
	case flterrors.ErrResourceNameIsNil:
		return server.ReplaceCertificateSigningRequest400JSONResponse{Message: err.Error()}, nil
	case flterrors.ErrResourceNotFound:
		return server.ReplaceCertificateSigningRequest404JSONResponse{}, nil
	case flterrors.ErrNoRowsUpdated, flterrors.ErrResourceVersionConflict:
		return server.ReplaceCertificateSigningRequest409JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (POST /api/v1/certificatesigningrequests/{name}/approval)
// NOTE: Approval currently also issues a certificate - this will change in the future based on policy
func (h *ServiceHandler) ApproveCertificateSigningRequest(ctx context.Context, request server.ApproveCertificateSigningRequestRequestObject) (server.ApproveCertificateSigningRequestResponseObject, error) {
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "certificatesigningrequests/approval", "post")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.ApproveCertificateSigningRequest503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.ApproveCertificateSigningRequest403JSONResponse{Message: Forbidden}, nil
	}
	orgId := store.NullOrgId

	storeCsr := h.store.CertificateSigningRequest()
	csr, err := storeCsr.Get(ctx, orgId, request.Name)
	if err != nil {
		if errors.Is(err, flterrors.ErrResourceNotFound) {
			return server.ApproveCertificateSigningRequest404JSONResponse{}, nil
		}
		return nil, err
	}

	// do not approve a denied request, or recreate a cert for an already-approved request
	if api.IsStatusConditionTrue(csr.Status.Conditions, api.CertificateSigningRequestDenied) {
		return server.ApproveCertificateSigningRequest409JSONResponse{Message: "The request has already been denied"}, nil
	}
	if api.IsStatusConditionTrue(csr.Status.Conditions, api.CertificateSigningRequestApproved) {
		return server.ApproveCertificateSigningRequest200JSONResponse{}, nil
	}

	signedCert, err := signApprovedCertificateSigningRequest(h.ca, *csr)
	if err != nil {
		switch {
		case errors.Is(err, flterrors.ErrCNLength) ||
			errors.Is(err, flterrors.ErrInvalidPEMBlock) ||
			errors.Is(err, flterrors.ErrSignature) ||
			errors.Is(err, flterrors.ErrCSRParse) ||
			errors.Is(err, flterrors.ErrSignCert) ||
			errors.Is(err, flterrors.ErrEncodeCert):
			return server.ApproveCertificateSigningRequest400JSONResponse{Message: err.Error()}, nil
		default:
			return nil, err
		}
	}

	approvedCondition := api.Condition{
		Type:    api.CertificateSigningRequestApproved,
		Status:  api.ConditionStatusTrue,
		Reason:  "Approved",
		Message: "Approved",
	}

	csr.Status.Certificate = &signedCert
	api.SetStatusCondition(&csr.Status.Conditions, approvedCondition)
	_, err = storeCsr.UpdateStatus(ctx, orgId, csr)

	switch err {
	case nil:
		return server.ApproveCertificateSigningRequest200JSONResponse{}, nil
	case flterrors.ErrResourceNotFound:
		return server.ApproveCertificateSigningRequest404JSONResponse{Message: err.Error()}, nil
	case flterrors.ErrNoRowsUpdated, flterrors.ErrResourceVersionConflict:
		return server.ApproveCertificateSigningRequest409JSONResponse{Message: err.Error()}, nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/certificatesigningrequests/{name}/approval)
func (h *ServiceHandler) DenyCertificateSigningRequest(ctx context.Context, request server.DenyCertificateSigningRequestRequestObject) (server.DenyCertificateSigningRequestResponseObject, error) {
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "certificatesigningrequests/approval", "delete")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.DenyCertificateSigningRequest503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.DenyCertificateSigningRequest403JSONResponse{Message: Forbidden}, nil
	}
	orgId := store.NullOrgId
	approvedCondition := api.Condition{
		Type:    api.CertificateSigningRequestApproved,
		Status:  api.ConditionStatusFalse,
		Reason:  "Denied",
		Message: "Denied",
	}
	deniedCondition := api.Condition{
		Type:    api.CertificateSigningRequestDenied,
		Status:  api.ConditionStatusTrue,
		Reason:  "Denied",
		Message: "Denied",
	}
	err = h.store.CertificateSigningRequest().UpdateConditions(ctx, orgId, request.Name, []api.Condition{approvedCondition, deniedCondition})

	switch err {
	case nil:
		return server.DenyCertificateSigningRequest200JSONResponse{}, nil
	case flterrors.ErrResourceNotFound:
		return server.DenyCertificateSigningRequest404JSONResponse{}, nil
	case flterrors.ErrNoRowsUpdated, flterrors.ErrResourceVersionConflict:
		return server.DenyCertificateSigningRequest409JSONResponse{}, nil
	default:
		return nil, err
	}
}
