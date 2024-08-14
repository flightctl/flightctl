package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/go-openapi/swag"
	"k8s.io/apimachinery/pkg/labels"
)

// (DELETE /api/v1/certificatesigningrequests)
func (h *ServiceHandler) DeleteCertificateSigningRequests(ctx context.Context, request server.DeleteCertificateSigningRequestsRequestObject) (server.DeleteCertificateSigningRequestsResponseObject, error) {
	orgId := store.NullOrgId

	err := h.store.CertificateSigningRequest().DeleteAll(ctx, orgId)
	switch err {
	case nil:
		return server.DeleteCertificateSigningRequests200JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (GET /api/v1/certificatesigningrequests)
func (h *ServiceHandler) ListCertificateSigningRequests(ctx context.Context, request server.ListCertificateSigningRequestsRequestObject) (server.ListCertificateSigningRequestsResponseObject, error) {
	orgId := store.NullOrgId
	labelSelector := ""
	if request.Params.LabelSelector != nil {
		labelSelector = *request.Params.LabelSelector
	}

	labelMap, err := labels.ConvertSelectorToLabelsMap(labelSelector)
	if err != nil {
		return server.ListCertificateSigningRequests400JSONResponse{Message: err.Error()}, nil
	}

	cont, err := store.ParseContinueString(request.Params.Continue)
	if err != nil {
		return server.ListCertificateSigningRequests400JSONResponse{Message: fmt.Sprintf("failed to parse continue parameter: %v", err)}, nil
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
		return server.ListCertificateSigningRequests400JSONResponse{Message: fmt.Sprintf("limit cannot exceed %d", store.MaxRecordsPerListRequest)}, nil
	}

	result, err := h.store.CertificateSigningRequest().List(ctx, orgId, listParams)
	switch err {
	case nil:
		return server.ListCertificateSigningRequests200JSONResponse(*result), nil
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
	orgId := store.NullOrgId

	err := h.store.CertificateSigningRequest().Delete(ctx, orgId, request.Name)
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

	result, _, err := h.store.CertificateSigningRequest().CreateOrUpdate(ctx, orgId, newObj)

	switch err {
	case nil:
		return server.PatchCertificateSigningRequest200JSONResponse(*result), nil
	case flterrors.ErrResourceIsNil, flterrors.ErrResourceNameIsNil:
		return server.PatchCertificateSigningRequest400JSONResponse{Message: err.Error()}, nil
	case flterrors.ErrResourceNotFound:
		return server.PatchCertificateSigningRequest404JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (PUT /api/v1/certificatesigningrequests/{name})
func (h *ServiceHandler) ReplaceCertificateSigningRequest(ctx context.Context, request server.ReplaceCertificateSigningRequestRequestObject) (server.ReplaceCertificateSigningRequestResponseObject, error) {
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
	default:
		return nil, err
	}
}

// (POST /api/v1/certificatesigningrequests/{name}/approval)
func (h *ServiceHandler) ApproveCertificateSigningRequest(ctx context.Context, request server.ApproveCertificateSigningRequestRequestObject) (server.ApproveCertificateSigningRequestResponseObject, error) {
	orgId := store.NullOrgId

	csr, err := h.store.CertificateSigningRequest().Get(ctx, orgId, request.Name)
	if err != nil {
		if errors.Is(err, flterrors.ErrResourceNotFound) {
			return server.ApproveCertificateSigningRequest404JSONResponse{}, nil
		} else {
			return nil, err
		}
	}

	if api.IsStatusConditionTrue(csr.Status.Conditions, api.CertificateSigningRequestDenied) {
		return server.ApproveCertificateSigningRequest409JSONResponse{Message: "The request has already been denied"}, nil
	}

	approvedCondition := api.Condition{
		Type:    api.CertificateSigningRequestApproved,
		Status:  api.ConditionStatusTrue,
		Reason:  "Approved",
		Message: "Approved",
	}
	err = h.store.CertificateSigningRequest().UpdateConditions(ctx, orgId, request.Name, []api.Condition{approvedCondition})

	switch err {
	case nil:
		return server.ApproveCertificateSigningRequest200JSONResponse{}, nil
	case flterrors.ErrResourceNotFound:
		return server.ApproveCertificateSigningRequest404JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/certificatesigningrequests/{name}/approval)
func (h *ServiceHandler) DenyCertificateSigningRequest(ctx context.Context, request server.DenyCertificateSigningRequestRequestObject) (server.DenyCertificateSigningRequestResponseObject, error) {
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
	err := h.store.CertificateSigningRequest().UpdateConditions(ctx, orgId, request.Name, []api.Condition{approvedCondition, deniedCondition})

	switch err {
	case nil:
		return server.DenyCertificateSigningRequest200JSONResponse{}, nil
	case flterrors.ErrResourceNotFound:
		return server.DenyCertificateSigningRequest404JSONResponse{}, nil
	default:
		return nil, err
	}
}
