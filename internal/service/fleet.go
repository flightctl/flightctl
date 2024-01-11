package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/server"
	"github.com/go-openapi/swag"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"k8s.io/apimachinery/pkg/labels"
)

type FleetStoreInterface interface {
	CreateFleet(ctx context.Context, orgId uuid.UUID, fleet *api.Fleet) (*api.Fleet, error)
	ListFleets(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.FleetList, error)
	GetFleet(ctx context.Context, orgId uuid.UUID, name string) (*api.Fleet, error)
	CreateOrUpdateFleet(ctx context.Context, orgId uuid.UUID, fleet *api.Fleet) (*api.Fleet, bool, error)
	UpdateFleetStatus(ctx context.Context, orgId uuid.UUID, fleet *api.Fleet) (*api.Fleet, error)
	DeleteFleets(ctx context.Context, orgId uuid.UUID) error
	DeleteFleet(ctx context.Context, orgId uuid.UUID, name string) error
}

func FleetFromReader(r io.Reader) (*api.Fleet, error) {
	var fleet api.Fleet
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&fleet)
	return &fleet, err
}

// (POST /api/v1/fleets)
func (h *ServiceHandler) CreateFleet(ctx context.Context, request server.CreateFleetRequestObject) (server.CreateFleetResponseObject, error) {
	orgId := NullOrgId

	result, err := h.fleetStore.CreateFleet(ctx, orgId, request.Body)
	switch err {
	case nil:
		return server.CreateFleet201JSONResponse(*result), nil
	default:
		return nil, err
	}
}

// (GET /api/v1/fleets)
func (h *ServiceHandler) ListFleets(ctx context.Context, request server.ListFleetsRequestObject) (server.ListFleetsResponseObject, error) {
	orgId := NullOrgId
	labelSelector := ""
	if request.Params.LabelSelector != nil {
		labelSelector = *request.Params.LabelSelector
	}

	labelMap, err := labels.ConvertSelectorToLabelsMap(labelSelector)
	if err != nil {
		return nil, err
	}

	cont, err := ParseContinueString(request.Params.Continue)
	if err != nil {
		return server.ListFleets400Response{}, fmt.Errorf("failed to parse continue parameter: %s", err)
	}

	listParams := ListParams{
		Labels:   labelMap,
		Limit:    int(swag.Int32Value(request.Params.Limit)),
		Continue: cont,
	}
	if listParams.Limit == 0 {
		listParams.Limit = MaxRecordsPerListRequest
	}
	if listParams.Limit > MaxRecordsPerListRequest {
		return server.ListFleets400Response{}, fmt.Errorf("limit cannot exceed %d", MaxRecordsPerListRequest)
	}

	result, err := h.fleetStore.ListFleets(ctx, orgId, listParams)
	switch err {
	case nil:
		return server.ListFleets200JSONResponse(*result), nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/fleets)
func (h *ServiceHandler) DeleteFleets(ctx context.Context, request server.DeleteFleetsRequestObject) (server.DeleteFleetsResponseObject, error) {
	orgId := NullOrgId

	err := h.fleetStore.DeleteFleets(ctx, orgId)
	switch err {
	case nil:
		return server.DeleteFleets200JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (GET /api/v1/fleets/{name})
func (h *ServiceHandler) ReadFleet(ctx context.Context, request server.ReadFleetRequestObject) (server.ReadFleetResponseObject, error) {
	orgId := NullOrgId

	result, err := h.fleetStore.GetFleet(ctx, orgId, request.Name)
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
	orgId := NullOrgId

	result, created, err := h.fleetStore.CreateOrUpdateFleet(ctx, orgId, request.Body)
	switch err {
	case nil:
		if created {
			return server.ReplaceFleet201JSONResponse(*result), nil
		} else {
			return server.ReplaceFleet200JSONResponse(*result), nil
		}
	case gorm.ErrRecordNotFound:
		return server.ReplaceFleet404Response{}, nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/fleets/{name})
func (h *ServiceHandler) DeleteFleet(ctx context.Context, request server.DeleteFleetRequestObject) (server.DeleteFleetResponseObject, error) {
	orgId := NullOrgId

	err := h.fleetStore.DeleteFleet(ctx, orgId, request.Name)
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
	orgId := NullOrgId

	result, err := h.fleetStore.GetFleet(ctx, orgId, request.Name)
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
	orgId := NullOrgId

	result, err := h.fleetStore.UpdateFleetStatus(ctx, orgId, request.Body)
	switch err {
	case nil:
		return server.ReplaceFleetStatus200JSONResponse(*result), nil
	case gorm.ErrRecordNotFound:
		return server.ReplaceFleetStatus404Response{}, nil
	default:
		return nil, err
	}
}
