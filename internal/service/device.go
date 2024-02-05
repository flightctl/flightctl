package service

import (
	"context"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/server"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/go-openapi/swag"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"k8s.io/apimachinery/pkg/labels"
)

type DeviceStore interface {
	Create(ctx context.Context, orgId uuid.UUID, device *api.Device) (*api.Device, error)
	List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.DeviceList, error)
	Get(ctx context.Context, orgId uuid.UUID, name string) (*api.Device, error)
	CreateOrUpdate(ctx context.Context, orgId uuid.UUID, device *api.Device) (*api.Device, bool, error)
	UpdateStatus(ctx context.Context, orgId uuid.UUID, device *api.Device) (*api.Device, error)
	DeleteAll(ctx context.Context, orgId uuid.UUID) error
	Delete(ctx context.Context, orgId uuid.UUID, name string) error
	ListIgnoreOrg(map[string]string) ([]model.Device, error)
	UpdateIgnoreOrg(device *model.Device) error
}

// (POST /api/v1/devices)
func (h *ServiceHandler) CreateDevice(ctx context.Context, request server.CreateDeviceRequestObject) (server.CreateDeviceResponseObject, error) {
	orgId := NullOrgId

	result, err := h.deviceStore.Create(ctx, orgId, request.Body)
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
	labelSelector := ""
	if request.Params.LabelSelector != nil {
		labelSelector = *request.Params.LabelSelector
	}

	labelMap, err := labels.ConvertSelectorToLabelsMap(labelSelector)
	if err != nil {
		return nil, err
	}

	if request.Params.FleetName != nil {
		fleet, err := h.fleetStore.Get(ctx, orgId, *request.Params.FleetName)
		if err != nil {
			return server.ListDevices400Response{}, fmt.Errorf("fleet not found %q, %w", *request.Params.FleetName, err)
		}
		labelMap = util.MergeLabels(fleet.Spec.Selector.MatchLabels, labelMap)
	}

	cont, err := ParseContinueString(request.Params.Continue)
	if err != nil {
		return server.ListDevices400Response{}, fmt.Errorf("failed to parse continue parameter: %s", err)
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
		return server.ListDevices400Response{}, fmt.Errorf("limit cannot exceed %d", MaxRecordsPerListRequest)
	}

	result, err := h.deviceStore.List(ctx, orgId, listParams)
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

	err := h.deviceStore.DeleteAll(ctx, orgId)
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

	result, err := h.deviceStore.Get(ctx, orgId, request.Name)
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
	if request.Body.Metadata.Name == nil || request.Name != *request.Body.Metadata.Name {
		return server.ReplaceDevice400Response{}, nil
	}

	result, created, err := h.deviceStore.CreateOrUpdate(ctx, orgId, request.Body)
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

	err := h.deviceStore.Delete(ctx, orgId, request.Name)
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

	result, err := h.deviceStore.Get(ctx, orgId, request.Name)
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

	device := request.Body
	device.Status.UpdatedAt = util.TimeStampStringPtr()

	result, err := h.deviceStore.UpdateStatus(ctx, orgId, device)
	switch err {
	case nil:
		return server.ReplaceDeviceStatus200JSONResponse(*result), nil
	case gorm.ErrRecordNotFound:
		return server.ReplaceDeviceStatus404Response{}, nil
	default:
		return nil, err
	}
}
