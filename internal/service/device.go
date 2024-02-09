package service

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/go-openapi/swag"
	"gorm.io/gorm"
	"k8s.io/apimachinery/pkg/labels"
)

// (POST /api/v1/devices)
func (h *ServiceHandler) CreateDevice(ctx context.Context, request server.CreateDeviceRequestObject) (server.CreateDeviceResponseObject, error) {
	orgId := store.NullOrgId

	result, err := h.store.Device().Create(ctx, orgId, request.Body)
	switch err {
	case nil:
		return server.CreateDevice201JSONResponse(*result), nil
	default:
		return nil, err
	}
}

// (GET /api/v1/devices)
func (h *ServiceHandler) ListDevices(ctx context.Context, request server.ListDevicesRequestObject) (server.ListDevicesResponseObject, error) {
	orgId := store.NullOrgId
	labelSelector := ""
	if request.Params.LabelSelector != nil {
		labelSelector = *request.Params.LabelSelector
	}

	labelMap, err := labels.ConvertSelectorToLabelsMap(labelSelector)
	if err != nil {
		return nil, err
	}

	if request.Params.FleetName != nil {
		fleet, err := h.store.Fleet().Get(ctx, orgId, *request.Params.FleetName)
		if err != nil {
			return server.ListDevices400Response{}, fmt.Errorf("fleet not found %q, %w", *request.Params.FleetName, err)
		}
		labelMap = util.MergeLabels(fleet.Spec.Selector.MatchLabels, labelMap)
	}

	cont, err := store.ParseContinueString(request.Params.Continue)
	if err != nil {
		return server.ListDevices400Response{}, fmt.Errorf("failed to parse continue parameter: %s", err)
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
		return server.ListDevices400Response{}, fmt.Errorf("limit cannot exceed %d", store.MaxRecordsPerListRequest)
	}

	result, err := h.store.Device().List(ctx, orgId, listParams)
	switch err {
	case nil:
		return server.ListDevices200JSONResponse(*result), nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/devices)
func (h *ServiceHandler) DeleteDevices(ctx context.Context, request server.DeleteDevicesRequestObject) (server.DeleteDevicesResponseObject, error) {
	orgId := store.NullOrgId

	err := h.store.Device().DeleteAll(ctx, orgId)
	switch err {
	case nil:
		return server.DeleteDevices200JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (GET /api/v1/devices/{name})
func (h *ServiceHandler) ReadDevice(ctx context.Context, request server.ReadDeviceRequestObject) (server.ReadDeviceResponseObject, error) {
	orgId := store.NullOrgId

	result, err := h.store.Device().Get(ctx, orgId, request.Name)
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
	orgId := store.NullOrgId
	if request.Body.Metadata.Name == nil || request.Name != *request.Body.Metadata.Name {
		return server.ReplaceDevice400Response{}, nil
	}

	result, created, err := h.store.Device().CreateOrUpdate(ctx, orgId, request.Body)
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
	orgId := store.NullOrgId

	err := h.store.Device().Delete(ctx, orgId, request.Name)
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
	orgId := store.NullOrgId

	result, err := h.store.Device().Get(ctx, orgId, request.Name)
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
	orgId := store.NullOrgId

	device := request.Body
	device.Status.UpdatedAt = util.TimeStampStringPtr()

	result, err := h.store.Device().UpdateStatus(ctx, orgId, device)
	switch err {
	case nil:
		return server.ReplaceDeviceStatus200JSONResponse(*result), nil
	case gorm.ErrRecordNotFound:
		return server.ReplaceDeviceStatus404Response{}, nil
	default:
		return nil, err
	}
}
