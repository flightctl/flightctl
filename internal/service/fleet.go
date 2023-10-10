package service

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/model"
	"github.com/flightctl/flightctl/pkg/server"
	"github.com/google/uuid"
)

type FleetStoreInterface interface {
	CreateFleet(orgId uuid.UUID, req *model.Fleet) (*model.Fleet, error)
	ListFleets(orgId uuid.UUID) ([]model.Fleet, error)
	GetFleet(orgId uuid.UUID, name string) (*model.Fleet, error)
	UpdateFleet(orgId uuid.UUID, fleet *model.Fleet) (*model.Fleet, error)
	UpdateFleetStatus(orgId uuid.UUID, fleet *model.Fleet) (*model.Fleet, error)
	DeleteFleets(orgId uuid.UUID) error
	DeleteFleet(orgId uuid.UUID, name string) error
}

// (POST /api/v1/fleets)
func (h *ServiceHandler) CreateFleet(ctx context.Context, request server.CreateFleetRequestObject) (server.CreateFleetResponseObject, error) {
	orgId := NullOrgId
	if request.ContentType != "application/json" {
		return nil, fmt.Errorf("bad content type %s", request.ContentType)
	}

	newFleet, err := model.NewFleetFromApiResourceReader(request.Body)
	if err != nil {
		return nil, err
	}

	result, err := h.fleetStore.CreateFleet(orgId, newFleet)
	if err != nil {
		return nil, err
	}
	return server.CreateFleet201JSONResponse(result.ToApiResource()), nil
}

// (GET /api/v1/fleets)
func (h *ServiceHandler) ListFleets(ctx context.Context, request server.ListFleetsRequestObject) (server.ListFleetsResponseObject, error) {
	orgId := NullOrgId
	fleets, err := h.fleetStore.ListFleets(orgId)
	if err != nil {
		return nil, err
	}
	return server.ListFleets200JSONResponse(model.FleetList(fleets).ToApiResource()), nil
}

// (DELETE /api/v1/fleets)
func (h *ServiceHandler) DeleteFleets(ctx context.Context, request server.DeleteFleetsRequestObject) (server.DeleteFleetsResponseObject, error) {
	orgId := NullOrgId
	err := h.fleetStore.DeleteFleets(orgId)
	if err != nil {
		return nil, err
	}
	return server.DeleteFleets200JSONResponse{}, nil
}

// (GET /api/v1/fleets/{name})
func (h *ServiceHandler) ReadFleet(ctx context.Context, request server.ReadFleetRequestObject) (server.ReadFleetResponseObject, error) {
	orgId := NullOrgId
	fleet, err := h.fleetStore.GetFleet(orgId, request.Name)
	if err != nil {
		return nil, err
	}
	return server.ReadFleet200JSONResponse(fleet.ToApiResource()), nil
}

// (PUT /api/v1/fleets/{name})
func (h *ServiceHandler) ReplaceFleet(ctx context.Context, request server.ReplaceFleetRequestObject) (server.ReplaceFleetResponseObject, error) {
	orgId := NullOrgId
	if request.ContentType != "application/json" {
		return nil, fmt.Errorf("bad content type %s", request.ContentType)
	}

	updatedFleet, err := model.NewFleetFromApiResourceReader(request.Body)
	if err != nil {
		return nil, err
	}

	fleet, err := h.fleetStore.UpdateFleet(orgId, updatedFleet)
	if err != nil {
		return nil, err
	}
	return server.ReplaceFleet200JSONResponse(fleet.ToApiResource()), nil
}

// (DELETE /api/v1/fleets/{name})
func (h *ServiceHandler) DeleteFleet(ctx context.Context, request server.DeleteFleetRequestObject) (server.DeleteFleetResponseObject, error) {
	orgId := NullOrgId
	if err := h.fleetStore.DeleteFleet(orgId, request.Name); err != nil {
		return nil, err
	}
	return server.DeleteFleet200JSONResponse{}, nil
}

// (GET /api/v1/fleets/{name}/status)
func (h *ServiceHandler) ReadFleetStatus(ctx context.Context, request server.ReadFleetStatusRequestObject) (server.ReadFleetStatusResponseObject, error) {
	orgId := NullOrgId
	fleet, err := h.fleetStore.GetFleet(orgId, request.Name)
	if err != nil {
		return nil, err
	}
	return server.ReadFleetStatus200JSONResponse(fleet.ToApiResource()), nil
}

// (PUT /api/v1/fleets/{name}/status)
func (h *ServiceHandler) ReplaceFleetStatus(ctx context.Context, request server.ReplaceFleetStatusRequestObject) (server.ReplaceFleetStatusResponseObject, error) {
	orgId := NullOrgId
	if request.ContentType != "application/json" {
		return nil, fmt.Errorf("bad content type %s", request.ContentType)
	}

	updatedFleet, err := model.NewFleetFromApiResourceReader(request.Body)
	if err != nil {
		return nil, err
	}

	result, err := h.fleetStore.UpdateFleetStatus(orgId, updatedFleet)
	if err != nil {
		return nil, err
	}
	return server.ReplaceFleetStatus200JSONResponse(result.ToApiResource()), nil
}
