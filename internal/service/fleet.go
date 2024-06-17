package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/go-openapi/swag"
	"k8s.io/apimachinery/pkg/labels"
)

func FleetFromReader(r io.Reader) (*api.Fleet, error) {
	var fleet api.Fleet
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&fleet)
	return &fleet, err
}

// (POST /api/v1/fleets)
func (h *ServiceHandler) CreateFleet(ctx context.Context, request server.CreateFleetRequestObject) (server.CreateFleetResponseObject, error) {
	orgId := store.NullOrgId
	if request.Body.Metadata.Name == nil {
		return server.CreateFleet400JSONResponse{Message: "fleet name not specified"}, nil
	}

	err := ValidateSettings(request.Body.Spec.Template.Spec.Settings)
	if err != nil {
		return server.CreateFleet400JSONResponse{Message: err.Error()}, nil
	}

	// don't set fields that are managed by the service
	request.Body.Status = nil
	NilOutManagedObjectMetaProperties(&request.Body.Metadata)
	if request.Body.Spec.Template.Metadata != nil {
		NilOutManagedObjectMetaProperties(request.Body.Spec.Template.Metadata)
	}

	err = ValidateDiscriminators(request.Body.Spec.Template.Spec.Config)
	if err != nil {
		return server.CreateFleet400JSONResponse{Message: err.Error()}, nil
	}

	result, err := h.store.Fleet().Create(ctx, orgId, request.Body, h.taskManager.FleetUpdatedCallback)
	switch err {
	case nil:
		return server.CreateFleet201JSONResponse(*result), nil
	case flterrors.ErrResourceIsNil:
		return server.CreateFleet400JSONResponse{Message: err.Error()}, nil
	default:
		return nil, err
	}
}

// (GET /api/v1/fleets)
func (h *ServiceHandler) ListFleets(ctx context.Context, request server.ListFleetsRequestObject) (server.ListFleetsResponseObject, error) {
	orgId := store.NullOrgId
	labelSelector := ""
	if request.Params.LabelSelector != nil {
		labelSelector = *request.Params.LabelSelector
	}

	labelMap, err := labels.ConvertSelectorToLabelsMap(labelSelector)
	if err != nil {
		return nil, err
	}

	cont, err := store.ParseContinueString(request.Params.Continue)
	if err != nil {
		return server.ListFleets400JSONResponse{Message: fmt.Sprintf("failed to parse continue parameter: %v", err)}, nil
	}

	listParams := store.ListParams{
		Labels:   labelMap,
		Limit:    int(swag.Int32Value(request.Params.Limit)),
		Continue: cont,
		Owner:    request.Params.Owner,
	}
	if listParams.Limit == 0 {
		listParams.Limit = store.MaxRecordsPerListRequest
	}
	if listParams.Limit > store.MaxRecordsPerListRequest {
		return server.ListFleets400JSONResponse{Message: fmt.Sprintf("limit cannot exceed %d", store.MaxRecordsPerListRequest)}, nil
	}

	result, err := h.store.Fleet().List(ctx, orgId, listParams)
	switch err {
	case nil:
		return server.ListFleets200JSONResponse(*result), nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/fleets)
func (h *ServiceHandler) DeleteFleets(ctx context.Context, request server.DeleteFleetsRequestObject) (server.DeleteFleetsResponseObject, error) {
	orgId := store.NullOrgId

	err := h.store.Fleet().DeleteAll(ctx, orgId, h.taskManager.AllFleetsDeletedCallback)
	switch err {
	case nil:
		return server.DeleteFleets200JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (GET /api/v1/fleets/{name})
func (h *ServiceHandler) ReadFleet(ctx context.Context, request server.ReadFleetRequestObject) (server.ReadFleetResponseObject, error) {
	orgId := store.NullOrgId

	result, err := h.store.Fleet().Get(ctx, orgId, request.Name)
	switch err {
	case nil:
		return server.ReadFleet200JSONResponse(*result), nil
	case flterrors.ErrResourceNotFound:
		return server.ReadFleet404JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (PUT /api/v1/fleets/{name})
func (h *ServiceHandler) ReplaceFleet(ctx context.Context, request server.ReplaceFleetRequestObject) (server.ReplaceFleetResponseObject, error) {
	orgId := store.NullOrgId
	if request.Body.Metadata.Name == nil {
		return server.ReplaceFleet400JSONResponse{Message: "metadata.name not specified"}, nil
	}
	if request.Name != *request.Body.Metadata.Name {
		return server.ReplaceFleet400JSONResponse{Message: "resource name specified in metadata does not match name in path"}, nil
	}

	err := ValidateSettings(request.Body.Spec.Template.Spec.Settings)
	if err != nil {
		return server.ReplaceFleet400JSONResponse{Message: err.Error()}, nil
	}

	err = ValidateDiscriminators(request.Body.Spec.Template.Spec.Config)
	if err != nil {
		return server.ReplaceFleet400JSONResponse{Message: err.Error()}, nil
	}

	// don't overwrite fields that are managed by the service
	request.Body.Status = nil
	NilOutManagedObjectMetaProperties(&request.Body.Metadata)
	if request.Body.Spec.Template.Metadata != nil {
		NilOutManagedObjectMetaProperties(request.Body.Spec.Template.Metadata)
	}

	result, created, err := h.store.Fleet().CreateOrUpdate(ctx, orgId, request.Body, h.taskManager.FleetUpdatedCallback)
	switch err {
	case nil:
		if created {
			return server.ReplaceFleet201JSONResponse(*result), nil
		} else {
			return server.ReplaceFleet200JSONResponse(*result), nil
		}
	case flterrors.ErrResourceIsNil:
		return server.ReplaceFleet400JSONResponse{Message: err.Error()}, nil
	case flterrors.ErrResourceNameIsNil:
		return server.ReplaceFleet400JSONResponse{Message: err.Error()}, nil
	case flterrors.ErrResourceNotFound:
		return server.ReplaceFleet404JSONResponse{}, nil
	case flterrors.ErrUpdatingResourceWithOwnerNotAllowed:
		return server.ReplaceFleet409JSONResponse{Message: err.Error()}, nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/fleets/{name})
func (h *ServiceHandler) DeleteFleet(ctx context.Context, request server.DeleteFleetRequestObject) (server.DeleteFleetResponseObject, error) {
	orgId := store.NullOrgId

	f, err := h.store.Fleet().Get(ctx, orgId, request.Name)
	if err == flterrors.ErrResourceNotFound {
		return server.DeleteFleet404JSONResponse{}, nil
	}
	if f.Metadata.Owner != nil {
		// Can't delete via api
		return server.DeleteFleet409JSONResponse{Message: "could not delete fleet because it is owned by another resource"}, nil
	}

	err = h.store.Fleet().Delete(ctx, orgId, h.taskManager.FleetUpdatedCallback, request.Name)
	switch err {
	case nil:
		return server.DeleteFleet200JSONResponse{}, nil
	case flterrors.ErrResourceNotFound:
		return server.DeleteFleet404JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (GET /api/v1/fleets/{name}/status)
func (h *ServiceHandler) ReadFleetStatus(ctx context.Context, request server.ReadFleetStatusRequestObject) (server.ReadFleetStatusResponseObject, error) {
	orgId := store.NullOrgId

	result, err := h.store.Fleet().Get(ctx, orgId, request.Name)
	switch err {
	case nil:
		return server.ReadFleetStatus200JSONResponse(*result), nil
	case flterrors.ErrResourceNotFound:
		return server.ReadFleetStatus404JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (PUT /api/v1/fleets/{name}/status)
func (h *ServiceHandler) ReplaceFleetStatus(ctx context.Context, request server.ReplaceFleetStatusRequestObject) (server.ReplaceFleetStatusResponseObject, error) {
	orgId := store.NullOrgId

	result, err := h.store.Fleet().UpdateStatus(ctx, orgId, request.Body)
	switch err {
	case nil:
		return server.ReplaceFleetStatus200JSONResponse(*result), nil
	case flterrors.ErrResourceNotFound:
		return server.ReplaceFleetStatus404JSONResponse{}, nil
	default:
		return nil, err
	}
}
