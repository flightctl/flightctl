package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/go-openapi/swag"
	"gorm.io/gorm"
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
		return server.CreateFleet400Response{}, fmt.Errorf("fleet name not specified")
	}

	result, err := h.store.Fleet().Create(ctx, orgId, request.Body, h.taskManager.FleetUpdatedCallback)
	switch err {
	case nil:
		return server.CreateFleet201JSONResponse(*result), nil
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
		return server.ListFleets400Response{}, fmt.Errorf("failed to parse continue parameter: %s", err)
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
		return server.ListFleets400Response{}, fmt.Errorf("limit cannot exceed %d", store.MaxRecordsPerListRequest)
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
	case gorm.ErrRecordNotFound:
		return server.ReadFleet404Response{}, nil
	default:
		return nil, err
	}
}

// (PUT /api/v1/fleets/{name})
func (h *ServiceHandler) ReplaceFleet(ctx context.Context, request server.ReplaceFleetRequestObject) (server.ReplaceFleetResponseObject, error) {
	orgId := store.NullOrgId
	if request.Body.Metadata.Name == nil {
		return server.ReplaceFleet400Response{}, fmt.Errorf("fleet name not specified in metadata")
	}
	if request.Name != *request.Body.Metadata.Name {
		return server.ReplaceFleet400Response{}, fmt.Errorf("fleet name specified in metadata does not match name in path")
	}

	// Since this is an api call, we remove the Owner from the fleet - to avoid user override
	request.Body.Metadata.Owner = nil

	result, created, err := h.store.Fleet().CreateOrUpdate(ctx, orgId, request.Body, h.taskManager.FleetUpdatedCallback)
	switch err {
	case nil:
		if created {
			return server.ReplaceFleet201JSONResponse(*result), nil
		} else {
			return server.ReplaceFleet200JSONResponse(*result), nil
		}
	case gorm.ErrRecordNotFound:
		return server.ReplaceFleet404Response{}, nil
	case gorm.ErrInvalidData: // owner issue
		h.log.Infof("an attempt do update Fleet/%s was blocked due to ownership", request.Name)
		return server.ReplaceFleet409Response{}, nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/fleets/{name})
func (h *ServiceHandler) DeleteFleet(ctx context.Context, request server.DeleteFleetRequestObject) (server.DeleteFleetResponseObject, error) {
	orgId := store.NullOrgId

	f, err := h.store.Fleet().Get(ctx, orgId, request.Name)
	if err == gorm.ErrRecordNotFound {
		return server.DeleteFleet404Response{}, nil
	}
	if f.Metadata.Owner != nil {
		// Cant delete via api
		h.log.Infof("an attempt do delete Fleet/%s was blocked due to ownership", request.Name)
		return server.DeleteFleet409Response{}, nil
	}

	err = h.store.Fleet().Delete(ctx, orgId, h.taskManager.FleetUpdatedCallback, request.Name)
	switch err {
	case nil:
		return server.DeleteFleet200JSONResponse{}, nil
	case gorm.ErrRecordNotFound:
		return server.DeleteFleet404Response{}, nil
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
	case gorm.ErrRecordNotFound:
		return server.ReadFleetStatus404Response{}, nil
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
	case gorm.ErrRecordNotFound:
		return server.ReplaceFleetStatus404Response{}, nil
	default:
		return nil, err
	}
}
