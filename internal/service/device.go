package service

import (
	"context"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/model"
	"github.com/flightctl/flightctl/pkg/server"
	"github.com/google/uuid"
)

var (
	NullOrgId = uuid.MustParse("00000000-0000-0000-0000-000000000000")
)

type DeviceStoreInterface interface {
	CreateDevice(orgId uuid.UUID, name string) (model.Device, error)
	ListDevices(orgId uuid.UUID) ([]model.Device, error)
	GetDevice(orgId uuid.UUID, name string) (model.Device, error)
	WriteDeviceSpec(orgId uuid.UUID, name string, spec api.DeviceSpec) error
	WriteDeviceStatus(orgId uuid.UUID, name string, status api.DeviceStatus) error
	DeleteDevices(orgId uuid.UUID) error
	DeleteDevice(orgId uuid.UUID, name string) error
}

// (POST /api/v1/devices)
func (h *ServiceHandler) CreateDevice(ctx context.Context, request server.CreateDeviceRequestObject) (server.CreateDeviceResponseObject, error) {
	orgId := NullOrgId
	device, err := h.deviceStore.CreateDevice(orgId, uuid.New().String())
	if err != nil {
		return nil, fmt.Errorf("repo create: %w", err)
	}
	return server.CreateDevice201JSONResponse(DeviceModelToApi(device)), nil
}

// (GET /api/v1/devices)
func (h *ServiceHandler) ListDevices(ctx context.Context, request server.ListDevicesRequestObject) (server.ListDevicesResponseObject, error) {
	orgId := NullOrgId
	devices, err := h.deviceStore.ListDevices(orgId)
	if err != nil {
		return nil, fmt.Errorf("repo find: %w", err)
	}
	return server.ListDevices200JSONResponse(DeviceListModelToApi(devices)), nil
}

// (DELETE /api/v1/devices)
func (h *ServiceHandler) DeleteDevices(ctx context.Context, request server.DeleteDevicesRequestObject) (server.DeleteDevicesResponseObject, error) {
	orgId := NullOrgId
	err := h.deviceStore.DeleteDevices(orgId)
	if err != nil {
		return nil, fmt.Errorf("repo find: %w", err)
	}
	return server.DeleteDevices200JSONResponse{}, nil
}

// (GET /api/v1/devices/{name})
func (h *ServiceHandler) ReadDevice(ctx context.Context, request server.ReadDeviceRequestObject) (server.ReadDeviceResponseObject, error) {
	orgId := NullOrgId
	device, err := h.deviceStore.GetDevice(orgId, request.Name)
	if err != nil {
		return nil, fmt.Errorf("repo find: %w", err)
	}
	return server.ReadDevice200JSONResponse(DeviceModelToApi(device)), nil
}

// (PUT /api/v1/devices/{name})
func (h *ServiceHandler) ReplaceDevice(ctx context.Context, request server.ReplaceDeviceRequestObject) (server.ReplaceDeviceResponseObject, error) {
	orgId := NullOrgId
	if err := h.deviceStore.WriteDeviceStatus(orgId, request.Name, api.DeviceStatus{}); err != nil {
		return nil, fmt.Errorf("repo update: %w", err)
	}
	device := model.Device{}
	return server.ReplaceDevice200JSONResponse(DeviceModelToApi(device)), nil
}

// (DELETE /api/v1/devices/{name})
func (h *ServiceHandler) DeleteDevice(ctx context.Context, request server.DeleteDeviceRequestObject) (server.DeleteDeviceResponseObject, error) {
	orgId := NullOrgId
	if err := h.deviceStore.DeleteDevice(orgId, request.Name); err != nil {
		return nil, fmt.Errorf("repo delete: %w", err)
	}
	return server.DeleteDevice200JSONResponse{}, nil
}

// (GET /api/v1/devices/{name}/status)
func (h *ServiceHandler) ReadDeviceStatus(ctx context.Context, request server.ReadDeviceStatusRequestObject) (server.ReadDeviceStatusResponseObject, error) {
	orgId := NullOrgId
	device, err := h.deviceStore.GetDevice(orgId, request.Name)
	if err != nil {
		return nil, fmt.Errorf("repo find: %w", err)
	}
	return server.ReadDeviceStatus200JSONResponse(DeviceModelToApi(device)), nil
}

// (PUT /api/v1/devices/{name}/status)
func (h *ServiceHandler) ReplaceDeviceStatus(ctx context.Context, request server.ReplaceDeviceStatusRequestObject) (server.ReplaceDeviceStatusResponseObject, error) {
	orgId := NullOrgId
	if err := h.deviceStore.WriteDeviceStatus(orgId, request.Name, api.DeviceStatus{}); err != nil {
		return nil, fmt.Errorf("repo update: %w", err)
	}
	return server.ReplaceDeviceStatus200JSONResponse{}, nil
}
