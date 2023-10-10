package service

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/model"
	"github.com/flightctl/flightctl/pkg/server"
	"github.com/google/uuid"
)

var (
	NullOrgId = uuid.MustParse("00000000-0000-0000-0000-000000000000")
)

type DeviceStoreInterface interface {
	CreateDevice(orgId uuid.UUID, device *model.Device) (*model.Device, error)
	ListDevices(orgId uuid.UUID) ([]model.Device, error)
	GetDevice(orgId uuid.UUID, name string) (*model.Device, error)
	UpdateDevice(orgId uuid.UUID, device *model.Device) (*model.Device, error)
	UpdateDeviceStatus(orgId uuid.UUID, device *model.Device) (*model.Device, error)
	DeleteDevices(orgId uuid.UUID) error
	DeleteDevice(orgId uuid.UUID, name string) error
}

// (POST /api/v1/devices)
func (h *ServiceHandler) CreateDevice(ctx context.Context, request server.CreateDeviceRequestObject) (server.CreateDeviceResponseObject, error) {
	orgId := NullOrgId
	if request.ContentType != "application/json" {
		return nil, fmt.Errorf("bad content type %s", request.ContentType)
	}

	newDevice, err := model.NewDeviceFromApiResourceReader(request.Body)
	if err != nil {
		return nil, err
	}

	result, err := h.deviceStore.CreateDevice(orgId, newDevice)
	if err != nil {
		return nil, err
	}
	return server.CreateDevice201JSONResponse(result.ToApiResource()), nil
}

// (GET /api/v1/devices)
func (h *ServiceHandler) ListDevices(ctx context.Context, request server.ListDevicesRequestObject) (server.ListDevicesResponseObject, error) {
	orgId := NullOrgId
	devices, err := h.deviceStore.ListDevices(orgId)
	if err != nil {
		return nil, err
	}
	return server.ListDevices200JSONResponse(model.DeviceList(devices).ToApiResource()), nil
}

// (DELETE /api/v1/devices)
func (h *ServiceHandler) DeleteDevices(ctx context.Context, request server.DeleteDevicesRequestObject) (server.DeleteDevicesResponseObject, error) {
	orgId := NullOrgId
	err := h.deviceStore.DeleteDevices(orgId)
	if err != nil {
		return nil, err
	}
	return server.DeleteDevices200JSONResponse{}, nil
}

// (GET /api/v1/devices/{name})
func (h *ServiceHandler) ReadDevice(ctx context.Context, request server.ReadDeviceRequestObject) (server.ReadDeviceResponseObject, error) {
	orgId := NullOrgId
	device, err := h.deviceStore.GetDevice(orgId, request.Name)
	if err != nil {
		return nil, err
	}
	return server.ReadDevice200JSONResponse(device.ToApiResource()), nil
}

// (PUT /api/v1/devices/{name})
func (h *ServiceHandler) ReplaceDevice(ctx context.Context, request server.ReplaceDeviceRequestObject) (server.ReplaceDeviceResponseObject, error) {
	orgId := NullOrgId
	if request.ContentType != "application/json" {
		return nil, fmt.Errorf("bad content type %s", request.ContentType)
	}

	updatedDevice, err := model.NewDeviceFromApiResourceReader(request.Body)
	if err != nil {
		return nil, err
	}

	device, err := h.deviceStore.UpdateDevice(orgId, updatedDevice)
	if err != nil {
		return nil, err
	}
	return server.ReplaceDevice200JSONResponse(device.ToApiResource()), nil
}

// (DELETE /api/v1/devices/{name})
func (h *ServiceHandler) DeleteDevice(ctx context.Context, request server.DeleteDeviceRequestObject) (server.DeleteDeviceResponseObject, error) {
	orgId := NullOrgId
	if err := h.deviceStore.DeleteDevice(orgId, request.Name); err != nil {
		return nil, err
	}
	return server.DeleteDevice200JSONResponse{}, nil
}

// (GET /api/v1/devices/{name}/status)
func (h *ServiceHandler) ReadDeviceStatus(ctx context.Context, request server.ReadDeviceStatusRequestObject) (server.ReadDeviceStatusResponseObject, error) {
	orgId := NullOrgId
	device, err := h.deviceStore.GetDevice(orgId, request.Name)
	if err != nil {
		return nil, err
	}
	return server.ReadDeviceStatus200JSONResponse(device.ToApiResource()), nil
}

// (PUT /api/v1/devices/{name}/status)
func (h *ServiceHandler) ReplaceDeviceStatus(ctx context.Context, request server.ReplaceDeviceStatusRequestObject) (server.ReplaceDeviceStatusResponseObject, error) {
	orgId := NullOrgId
	if request.ContentType != "application/json" {
		return nil, fmt.Errorf("bad content type %s", request.ContentType)
	}

	updatedDevice, err := model.NewDeviceFromApiResourceReader(request.Body)
	if err != nil {
		return nil, err
	}

	result, err := h.deviceStore.UpdateDeviceStatus(orgId, updatedDevice)
	if err != nil {
		return nil, err
	}
	return server.ReplaceDeviceStatus200JSONResponse(result.ToApiResource()), nil
}
