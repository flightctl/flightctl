package service

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/model"
	"github.com/flightctl/flightctl/pkg/server"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type FleetStoreInterface interface {
	CreateFleet(orgId uuid.UUID, req *model.Fleet) (*model.Fleet, error)
	ListFleets(orgId uuid.UUID) ([]model.Fleet, error)
	GetFleet(orgId uuid.UUID, name string) (*model.Fleet, error)
	CreateOrUpdateFleet(orgId uuid.UUID, fleet *model.Fleet) (*model.Fleet, bool, error)
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
	switch err {
	case nil:
		return server.CreateFleet201JSONResponse(result.ToApiResource()), nil
	default:
		return nil, err
	}
}

// (GET /api/v1/fleets)
func (h *ServiceHandler) ListFleets(ctx context.Context, request server.ListFleetsRequestObject) (server.ListFleetsResponseObject, error) {
	orgId := NullOrgId
	fleets, err := h.fleetStore.ListFleets(orgId)
	switch err {
	case nil:
		return server.ListFleets200JSONResponse(model.FleetList(fleets).ToApiResource()), nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/fleets)
func (h *ServiceHandler) DeleteFleets(ctx context.Context, request server.DeleteFleetsRequestObject) (server.DeleteFleetsResponseObject, error) {
	orgId := NullOrgId
	err := h.fleetStore.DeleteFleets(orgId)
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
	fleet, err := h.fleetStore.GetFleet(orgId, request.Name)
	switch err {
	case nil:
		return server.ReadFleet200JSONResponse(fleet.ToApiResource()), nil
	case gorm.ErrRecordNotFound:
		return server.ReadFleet404Response{}, nil
	default:
		return nil, err
	}
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

	fleet, created, err := h.fleetStore.CreateOrUpdateFleet(orgId, updatedFleet)
	switch err {
	case nil:
		if created {
			return server.ReplaceFleet201JSONResponse(fleet.ToApiResource()), nil
		} else {
			return server.ReplaceFleet200JSONResponse(fleet.ToApiResource()), nil
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
	err := h.fleetStore.DeleteFleet(orgId, request.Name)
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
	fleet, err := h.fleetStore.GetFleet(orgId, request.Name)
	switch err {
	case nil:
		return server.ReadFleetStatus200JSONResponse(fleet.ToApiResource()), nil
	case gorm.ErrRecordNotFound:
		return server.ReadFleetStatus404Response{}, nil
	default:
		return nil, err
	}
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
	switch err {
	case nil:
		return server.ReplaceFleetStatus200JSONResponse(result.ToApiResource()), nil
	case gorm.ErrRecordNotFound:
		return server.ReplaceFleetStatus404Response{}, nil
	default:
		return nil, err
	}
}
