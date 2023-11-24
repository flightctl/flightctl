package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/server"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type DeviceStoreInterface interface {
	CreateDevice(orgId uuid.UUID, device *api.Device) (*api.Device, error)
	ListDevices(orgId uuid.UUID) (*api.DeviceList, error)
	GetDevice(orgId uuid.UUID, name string) (*api.Device, error)
	CreateOrUpdateDevice(orgId uuid.UUID, device *api.Device) (*api.Device, bool, error)
	UpdateDeviceStatus(orgId uuid.UUID, device *api.Device) (*api.Device, error)
	DeleteDevices(orgId uuid.UUID) error
	DeleteDevice(orgId uuid.UUID, name string) error
}

func DeviceFromReader(r io.Reader) (*api.Device, error) {
	var device api.Device
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&device)
	return &device, err
}

// (POST /api/v1/devices)
func (h *ServiceHandler) CreateDevice(ctx context.Context, request server.CreateDeviceRequestObject) (server.CreateDeviceResponseObject, error) {
	orgId := NullOrgId

	if request.ContentType != "application/json" {
		return nil, fmt.Errorf("bad content type %s", request.ContentType)
	}

	apiResource, err := DeviceFromReader(request.Body)
	if err != nil {
		return nil, err
	}

	result, err := h.deviceStore.CreateDevice(orgId, apiResource)
	switch err {
	case nil:
		return server.CreateDevice201JSONResponse(*result), nil
	default:
		return nil, err
	}
}

// (GET /api/v1/devices)
func (h *ServiceHandler) ListDevices(ctx context.Context, request server.ListDevicesRequestObject) (server.ListDevicesResponseObject, error) {
	orgId := NullOrgId

	result, err := h.deviceStore.ListDevices(orgId)
	switch err {
	case nil:
		return server.ListDevices200JSONResponse(*result), nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/devices)
func (h *ServiceHandler) DeleteDevices(ctx context.Context, request server.DeleteDevicesRequestObject) (server.DeleteDevicesResponseObject, error) {
	orgId := NullOrgId

	err := h.deviceStore.DeleteDevices(orgId)
	switch err {
	case nil:
		return server.DeleteDevices200JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (GET /api/v1/devices/{name})
func (h *ServiceHandler) ReadDevice(ctx context.Context, request server.ReadDeviceRequestObject) (server.ReadDeviceResponseObject, error) {
	orgId := NullOrgId

	result, err := h.deviceStore.GetDevice(orgId, request.Name)
	switch err {
	case nil:
		return server.ReadDevice200JSONResponse(*result), nil
	case gorm.ErrRecordNotFound:
		return server.ReadDevice404Response{}, nil
	default:
		return nil, err
	}
}

// (PUT /api/v1/devices/{name})
func (h *ServiceHandler) ReplaceDevice(ctx context.Context, request server.ReplaceDeviceRequestObject) (server.ReplaceDeviceResponseObject, error) {
	orgId := NullOrgId

	if request.ContentType != "application/json" {
		return nil, fmt.Errorf("bad content type %s", request.ContentType)
	}

	apiResource, err := DeviceFromReader(request.Body)
	if err != nil {
		return nil, err
	}

	result, created, err := h.deviceStore.CreateOrUpdateDevice(orgId, apiResource)
	switch err {
	case nil:
		if created {
			return server.ReplaceDevice201JSONResponse(*result), nil
		} else {
			return server.ReplaceDevice200JSONResponse(*result), nil
		}
	case gorm.ErrRecordNotFound:
		return server.ReplaceDevice404Response{}, nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/devices/{name})
func (h *ServiceHandler) DeleteDevice(ctx context.Context, request server.DeleteDeviceRequestObject) (server.DeleteDeviceResponseObject, error) {
	orgId := NullOrgId

	err := h.deviceStore.DeleteDevice(orgId, request.Name)
	switch err {
	case nil:
		return server.DeleteDevice200JSONResponse{}, nil
	case gorm.ErrRecordNotFound:
		return server.DeleteDevice404Response{}, nil
	default:
		return nil, err
	}
}

// (GET /api/v1/devices/{name}/status)
func (h *ServiceHandler) ReadDeviceStatus(ctx context.Context, request server.ReadDeviceStatusRequestObject) (server.ReadDeviceStatusResponseObject, error) {
	orgId := NullOrgId

	result, err := h.deviceStore.GetDevice(orgId, request.Name)
	switch err {
	case nil:
		return server.ReadDeviceStatus200JSONResponse(*result), nil
	case gorm.ErrRecordNotFound:
		return server.ReadDeviceStatus404Response{}, nil
	default:
		return nil, err
	}
}

// (PUT /api/v1/devices/{name}/status)
func (h *ServiceHandler) ReplaceDeviceStatus(ctx context.Context, request server.ReplaceDeviceStatusRequestObject) (server.ReplaceDeviceStatusResponseObject, error) {
	orgId := NullOrgId

	if request.ContentType != "application/json" {
		return nil, fmt.Errorf("bad content type %s", request.ContentType)
	}

	apiResource, err := DeviceFromReader(request.Body)
	if err != nil {
		return nil, err
	}

	result, err := h.deviceStore.UpdateDeviceStatus(orgId, apiResource)
	switch err {
	case nil:
		return server.ReplaceDeviceStatus200JSONResponse(*result), nil
	case gorm.ErrRecordNotFound:
		return server.ReplaceDeviceStatus404Response{}, nil
	default:
		return nil, err
	}
}
