package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	k8sselector "github.com/flightctl/flightctl/pkg/k8s/selector"
	"github.com/flightctl/flightctl/pkg/k8s/selector/fields"
	"github.com/go-openapi/swag"
	"k8s.io/apimachinery/pkg/labels"
)

// (POST /api/v1/resourcesyncs)
func (h *ServiceHandler) CreateResourceSync(ctx context.Context, request server.CreateResourceSyncRequestObject) (server.CreateResourceSyncResponseObject, error) {
	orgId := store.NullOrgId

	// don't set fields that are managed by the service
	request.Body.Status = nil
	common.NilOutManagedObjectMetaProperties(&request.Body.Metadata)

	if errs := request.Body.Validate(); len(errs) > 0 {
		return server.CreateResourceSync400JSONResponse{Message: errors.Join(errs...).Error()}, nil
	}

	result, err := h.store.ResourceSync().Create(ctx, orgId, request.Body)
	switch err {
	case nil:
		return server.CreateResourceSync201JSONResponse(*result), nil
	case flterrors.ErrResourceIsNil:
		return server.CreateResourceSync400JSONResponse{Message: err.Error()}, nil
	case flterrors.ErrDuplicateName:
		return server.CreateResourceSync400JSONResponse{Message: err.Error()}, nil
	default:
		return nil, err
	}
}

// (GET /api/v1/resourcesyncs)
func (h *ServiceHandler) ListResourceSync(ctx context.Context, request server.ListResourceSyncRequestObject) (server.ListResourceSyncResponseObject, error) {
	orgId := store.NullOrgId
	labelSelector := ""
	if request.Params.LabelSelector != nil {
		labelSelector = *request.Params.LabelSelector
	}

	labelMap, err := labels.ConvertSelectorToLabelsMap(labelSelector)
	if err != nil {
		return server.ListResourceSync400JSONResponse{Message: err.Error()}, nil
	}

	cont, err := store.ParseContinueString(request.Params.Continue)
	if err != nil {
		return server.ListResourceSync400JSONResponse{Message: fmt.Sprintf("failed to parse continue parameter: %v", err)}, nil
	}

	var fieldSelector k8sselector.Selector
	if request.Params.FieldSelector != nil {
		if fieldSelector, err = fields.ParseSelector(*request.Params.FieldSelector); err != nil {
			return server.ListResourceSync400JSONResponse{Message: fmt.Sprintf("failed to parse field selector: %v", err)}, nil
		}
	}

	var sortField *store.SortField
	if request.Params.SortBy != nil {
		sortField = &store.SortField{
			FieldName: selector.SelectorFieldName(*request.Params.SortBy),
			Order:     *request.Params.SortOrder,
		}
	}

	listParams := store.ListParams{
		Labels:        labelMap,
		Limit:         int(swag.Int32Value(request.Params.Limit)),
		Continue:      cont,
		FieldSelector: fieldSelector,
		SortBy:        sortField,
	}
	if listParams.Limit == 0 {
		listParams.Limit = store.MaxRecordsPerListRequest
	}
	if listParams.Limit > store.MaxRecordsPerListRequest {
		return server.ListResourceSync400JSONResponse{Message: fmt.Sprintf("limit cannot exceed %d", store.MaxRecordsPerListRequest)}, nil
	}

	if request.Params.Repository != nil {
		specFilter := []string{fmt.Sprintf("spec.repository=%s", *request.Params.Repository)}
		filterMap, err := ConvertFieldFilterParamsToMap(specFilter)
		if err != nil {
			return server.ListResourceSync400JSONResponse{Message: fmt.Sprintf("failed to convert repository filter: %v", err)}, nil
		}
		listParams.Filter = filterMap
	}

	result, err := h.store.ResourceSync().List(ctx, orgId, listParams)
	if err == nil {
		return server.ListResourceSync200JSONResponse(*result), nil
	}

	var se *selector.SelectorError

	switch {
	case selector.AsSelectorError(err, &se):
		return server.ListResourceSync400JSONResponse{Message: se.Error()}, nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/resourcesyncs)
func (h *ServiceHandler) DeleteResourceSyncs(ctx context.Context, request server.DeleteResourceSyncsRequestObject) (server.DeleteResourceSyncsResponseObject, error) {
	orgId := store.NullOrgId

	err := h.store.ResourceSync().DeleteAll(ctx, orgId, h.store.Fleet().UnsetOwnerByKind)
	switch err {
	case nil:
		return server.DeleteResourceSyncs200JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (GET /api/v1/resourcesyncs/{name})
func (h *ServiceHandler) ReadResourceSync(ctx context.Context, request server.ReadResourceSyncRequestObject) (server.ReadResourceSyncResponseObject, error) {
	orgId := store.NullOrgId

	result, err := h.store.ResourceSync().Get(ctx, orgId, request.Name)
	switch err {
	case nil:
		return server.ReadResourceSync200JSONResponse(*result), nil
	case flterrors.ErrResourceNotFound:
		return server.ReadResourceSync404JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (PUT /api/v1/resourcesyncs/{name})
func (h *ServiceHandler) ReplaceResourceSync(ctx context.Context, request server.ReplaceResourceSyncRequestObject) (server.ReplaceResourceSyncResponseObject, error) {
	orgId := store.NullOrgId

	// don't overwrite fields that are managed by the service
	request.Body.Status = nil
	common.NilOutManagedObjectMetaProperties(&request.Body.Metadata)
	if errs := request.Body.Validate(); len(errs) > 0 {
		return server.ReplaceResourceSync400JSONResponse{Message: errors.Join(errs...).Error()}, nil
	}
	if request.Name != *request.Body.Metadata.Name {
		return server.ReplaceResourceSync400JSONResponse{Message: "resource name specified in metadata does not match name in path"}, nil
	}

	result, created, err := h.store.ResourceSync().CreateOrUpdate(ctx, orgId, request.Body)
	switch err {
	case nil:
		if created {
			return server.ReplaceResourceSync201JSONResponse(*result), nil
		} else {
			return server.ReplaceResourceSync200JSONResponse(*result), nil
		}
	case flterrors.ErrResourceIsNil:
		return server.ReplaceResourceSync400JSONResponse{Message: err.Error()}, nil
	case flterrors.ErrResourceNameIsNil:
		return server.ReplaceResourceSync400JSONResponse{Message: err.Error()}, nil
	case flterrors.ErrResourceNotFound:
		return server.ReplaceResourceSync404JSONResponse{}, nil
	case flterrors.ErrNoRowsUpdated, flterrors.ErrResourceVersionConflict:
		return server.ReplaceResourceSync409JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/resourcesyncs/{name})
func (h *ServiceHandler) DeleteResourceSync(ctx context.Context, request server.DeleteResourceSyncRequestObject) (server.DeleteResourceSyncResponseObject, error) {
	orgId := store.NullOrgId
	err := h.store.ResourceSync().Delete(ctx, orgId, request.Name, h.store.Fleet().UnsetOwner)
	switch err {
	case nil:
		return server.DeleteResourceSync200JSONResponse{}, nil
	case flterrors.ErrResourceNotFound:
		return server.DeleteResourceSync404JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (PATCH /api/v1/resourcesyncs/{name})
// Only metadata.labels and spec can be patched. If we try to patch other fields, HTTP 400 Bad Request is returned.
func (h *ServiceHandler) PatchResourceSync(ctx context.Context, request server.PatchResourceSyncRequestObject) (server.PatchResourceSyncResponseObject, error) {
	orgId := store.NullOrgId

	currentObj, err := h.store.ResourceSync().Get(ctx, orgId, request.Name)
	if err != nil {
		switch err {
		case flterrors.ErrResourceIsNil, flterrors.ErrResourceNameIsNil:
			return server.PatchResourceSync400JSONResponse{Message: err.Error()}, nil
		case flterrors.ErrResourceNotFound:
			return server.PatchResourceSync404JSONResponse{}, nil
		default:
			return nil, err
		}
	}

	newObj := &v1alpha1.ResourceSync{}
	err = ApplyJSONPatch(ctx, currentObj, newObj, *request.Body, "/api/v1/resourcesyncs/"+request.Name)
	if err != nil {
		return server.PatchResourceSync400JSONResponse{Message: err.Error()}, nil
	}

	if newObj.Metadata.Name == nil || *currentObj.Metadata.Name != *newObj.Metadata.Name {
		return server.PatchResourceSync400JSONResponse{Message: "metadata.name is immutable"}, nil
	}
	if currentObj.ApiVersion != newObj.ApiVersion {
		return server.PatchResourceSync400JSONResponse{Message: "apiVersion is immutable"}, nil
	}
	if currentObj.Kind != newObj.Kind {
		return server.PatchResourceSync400JSONResponse{Message: "kind is immutable"}, nil
	}
	if !reflect.DeepEqual(currentObj.Status, newObj.Status) {
		return server.PatchResourceSync400JSONResponse{Message: "status is immutable"}, nil
	}

	common.NilOutManagedObjectMetaProperties(&newObj.Metadata)
	newObj.Metadata.ResourceVersion = nil
	result, err := h.store.ResourceSync().Update(ctx, orgId, newObj)

	switch err {
	case nil:
		return server.PatchResourceSync200JSONResponse(*result), nil
	case flterrors.ErrResourceIsNil, flterrors.ErrResourceNameIsNil:
		return server.PatchResourceSync400JSONResponse{Message: err.Error()}, nil
	case flterrors.ErrResourceNotFound:
		return server.PatchResourceSync404JSONResponse{}, nil
	case flterrors.ErrNoRowsUpdated, flterrors.ErrResourceVersionConflict:
		return server.PatchResourceSync409JSONResponse{}, nil
	default:
		return nil, err
	}
}
