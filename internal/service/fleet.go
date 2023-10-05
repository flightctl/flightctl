package service

import (
	"context"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/model"
	"github.com/flightctl/flightctl/pkg/server"
	"github.com/google/uuid"
)

type FleetStoreInterface interface {
	CreateFleet(orgId uuid.UUID, name string) (model.Fleet, error)
	ListFleets(orgId uuid.UUID) ([]model.Fleet, error)
	GetFleet(orgId uuid.UUID, name string) (model.Fleet, error)
	WriteFleetSpec(orgId uuid.UUID, name string, spec api.FleetSpec) error
	WriteFleetStatus(orgId uuid.UUID, name string, status api.FleetStatus) error
	DeleteFleets(orgId uuid.UUID) error
	DeleteFleet(orgId uuid.UUID, name string) error
}

// (DELETE /api/v1/fleets)
func (h *ServiceHandler) DeleteFleets(ctx context.Context, request server.DeleteFleetsRequestObject) (server.DeleteFleetsResponseObject, error) {
	return nil, nil
}

// (GET /api/v1/fleets)
func (h *ServiceHandler) ListFleets(ctx context.Context, request server.ListFleetsRequestObject) (server.ListFleetsResponseObject, error) {
	return nil, nil
}

// (POST /api/v1/fleets)
func (h *ServiceHandler) CreateFleet(ctx context.Context, request server.CreateFleetRequestObject) (server.CreateFleetResponseObject, error) {
	return nil, nil
}

// (DELETE /api/v1/fleets/{name})
func (h *ServiceHandler) DeleteFleet(ctx context.Context, request server.DeleteFleetRequestObject) (server.DeleteFleetResponseObject, error) {
	return nil, nil
}

// (GET /api/v1/fleets/{name})
func (h *ServiceHandler) ReadFleet(ctx context.Context, request server.ReadFleetRequestObject) (server.ReadFleetResponseObject, error) {
	return nil, nil
}

// (PUT /api/v1/fleets/{name})
func (h *ServiceHandler) ReplaceFleet(ctx context.Context, request server.ReplaceFleetRequestObject) (server.ReplaceFleetResponseObject, error) {
	return nil, nil
}

// (GET /api/v1/fleets/{name}/status)
func (h *ServiceHandler) ReadFleetStatus(ctx context.Context, request server.ReadFleetStatusRequestObject) (server.ReadFleetStatusResponseObject, error) {
	return nil, nil
}

// (PUT /api/v1/fleets/{name}/status)
func (h *ServiceHandler) ReplaceFleetStatus(ctx context.Context, request server.ReplaceFleetStatusRequestObject) (server.ReplaceFleetStatusResponseObject, error) {
	return nil, nil
}
