package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/go-openapi/swag"
	"github.com/google/uuid"
)

// (POST /api/v1/devices)
func (h *ServiceHandler) CreateDevice(ctx context.Context, request server.CreateDeviceRequestObject) (server.CreateDeviceResponseObject, error) {
	if request.Body.Spec != nil && request.Body.Spec.Decommissioning != nil {
		h.log.WithError(flterrors.ErrDecommission).Error("attempt to create decommissioned device")
		return server.CreateDevice400JSONResponse(api.StatusBadRequest(flterrors.ErrDecommission.Error())), nil
	}

	orgId := store.NullOrgId

	// don't set fields that are managed by the service
	request.Body.Status = nil
	common.NilOutManagedObjectMetaProperties(&request.Body.Metadata)

	if errs := request.Body.Validate(); len(errs) > 0 {
		return server.CreateDevice400JSONResponse(api.StatusBadRequest(errors.Join(errs...).Error())), nil
	}

	common.UpdateServiceSideStatus(ctx, h.store, h.log, orgId, request.Body)

	result, err := h.store.Device().Create(ctx, orgId, request.Body, h.callbackManager.DeviceUpdatedCallback)
	switch {
	case err == nil:
		return server.CreateDevice201JSONResponse(*result), nil
	case errors.Is(err, flterrors.ErrResourceIsNil), errors.Is(err, flterrors.ErrIllegalResourceVersionFormat):
		return server.CreateDevice400JSONResponse(api.StatusBadRequest(err.Error())), nil
	case errors.Is(err, flterrors.ErrDuplicateName):
		return server.CreateDevice409JSONResponse(api.StatusResourceVersionConflict(err.Error())), nil
	default:
		return nil, err
	}
}

// (GET /api/v1/devices)
func (h *ServiceHandler) ListDevices(ctx context.Context, request server.ListDevicesRequestObject) (server.ListDevicesResponseObject, error) {
	orgId := store.NullOrgId

	var (
		fieldSelector *selector.FieldSelector
		err           error
	)
	if request.Params.FieldSelector != nil {
		if fieldSelector, err = selector.NewFieldSelector(*request.Params.FieldSelector); err != nil {
			return server.ListDevices400JSONResponse(api.StatusBadRequest(fmt.Sprintf("failed to parse field selector: %v", err))), nil
		}
	}

	var labelSelector *selector.LabelSelector
	if request.Params.LabelSelector != nil {
		if labelSelector, err = selector.NewLabelSelector(*request.Params.LabelSelector); err != nil {
			return server.ListDevices400JSONResponse(api.StatusBadRequest(fmt.Sprintf("failed to parse label selector: %v", err))), nil
		}
	}

	// Check if SummaryOnly is true
	if request.Params.SummaryOnly != nil && *request.Params.SummaryOnly {
		// Check for unsupported parameters
		if request.Params.Limit != nil || request.Params.Continue != nil {
			return server.ListDevices400JSONResponse(api.StatusBadRequest("parameters such as 'limit', and 'continue' are not supported when 'summaryOnly' is true")), nil
		}

		result, err := h.store.Device().Summary(ctx, orgId, store.ListParams{
			FieldSelector: fieldSelector,
			LabelSelector: labelSelector,
		})

		switch err {
		case nil:
			// Create an empty DeviceList and set the summary
			emptyList, _ := model.DevicesToApiResource([]model.Device{}, nil, nil)
			emptyList.Summary = result
			return server.ListDevices200JSONResponse(emptyList), nil
		default:
			return nil, err
		}
	}

	cont, err := store.ParseContinueString(request.Params.Continue)
	if err != nil {
		return server.ListDevices400JSONResponse(api.StatusBadRequest(fmt.Sprintf("failed to parse continue parameter: %v", err))), nil
	}

	listParams := store.ListParams{
		Limit:         int(swag.Int32Value(request.Params.Limit)),
		Continue:      cont,
		FieldSelector: fieldSelector,
		LabelSelector: labelSelector,
	}
	if listParams.Limit == 0 {
		listParams.Limit = store.MaxRecordsPerListRequest
	}
	if listParams.Limit > store.MaxRecordsPerListRequest {
		return server.ListDevices400JSONResponse(api.StatusBadRequest(fmt.Sprintf("limit cannot exceed %d", store.MaxRecordsPerListRequest))), nil
	}

	result, err := h.store.Device().List(ctx, orgId, listParams)
	if err == nil {
		return server.ListDevices200JSONResponse(*result), nil
	}

	var se *selector.SelectorError

	switch {
	case errors.Is(err, flterrors.ErrLimitParamOutOfBounds):
		return server.ListDevices400JSONResponse(api.StatusBadRequest(err.Error())), nil
	case selector.AsSelectorError(err, &se):
		return server.ListDevices400JSONResponse(api.StatusBadRequest(se.Error())), nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/devices)
func (h *ServiceHandler) DeleteDevices(ctx context.Context, request server.DeleteDevicesRequestObject) (server.DeleteDevicesResponseObject, error) {
	orgId := store.NullOrgId

	err := h.store.Device().DeleteAll(ctx, orgId, h.callbackManager.AllDevicesDeletedCallback)
	switch err {
	case nil:
		return server.DeleteDevices200JSONResponse(api.StatusOK()), nil
	default:
		return nil, err
	}
}

// (GET /api/v1/devices/{name})
func (h *ServiceHandler) ReadDevice(ctx context.Context, request server.ReadDeviceRequestObject) (server.ReadDeviceResponseObject, error) {
	orgId := store.NullOrgId

	result, err := h.store.Device().Get(ctx, orgId, request.Name)
	switch {
	case err == nil:
		return server.ReadDevice200JSONResponse(*result), nil
	case errors.Is(err, flterrors.ErrResourceNotFound):
		return server.ReadDevice404JSONResponse(api.StatusResourceNotFound("Device", request.Name)), nil
	default:
		return nil, err
	}
}

func DeviceVerificationCallback(before, after *api.Device) error {
	// Ensure the device wasn't decommissioned
	if before != nil && before.Spec != nil && before.Spec.Decommissioning != nil {
		return flterrors.ErrDecommission
	}
	return nil
}

// (PUT /api/v1/devices/{name})
func (h *ServiceHandler) ReplaceDevice(ctx context.Context, request server.ReplaceDeviceRequestObject) (server.ReplaceDeviceResponseObject, error) {
	if request.Body.Spec != nil && request.Body.Spec.Decommissioning != nil {
		h.log.WithError(flterrors.ErrDecommission).Error("attempt to set decommissioned status when replacing device, or to replace decommissioned device")
		return server.ReplaceDevice400JSONResponse(api.StatusBadRequest(flterrors.ErrDecommission.Error())), nil
	}

	orgId := store.NullOrgId

	// don't overwrite fields that are managed by the service
	request.Body.Status = nil
	common.NilOutManagedObjectMetaProperties(&request.Body.Metadata)

	if errs := request.Body.Validate(); len(errs) > 0 {
		return server.ReplaceDevice400JSONResponse(api.StatusBadRequest(errors.Join(errs...).Error())), nil
	}
	if request.Name != *request.Body.Metadata.Name {
		return server.ReplaceDevice400JSONResponse(api.StatusBadRequest("resource name specified in metadata does not match name in path")), nil
	}

	common.UpdateServiceSideStatus(ctx, h.store, h.log, orgId, request.Body)

	result, created, err := h.store.Device().CreateOrUpdate(ctx, orgId, request.Body, nil, true, DeviceVerificationCallback, h.callbackManager.DeviceUpdatedCallback)
	switch {
	case err == nil:
		if created {
			return server.ReplaceDevice201JSONResponse(*result), nil
		} else {
			return server.ReplaceDevice200JSONResponse(*result), nil
		}
	case errors.Is(err, flterrors.ErrResourceIsNil):
		return server.ReplaceDevice400JSONResponse(api.StatusBadRequest(err.Error())), nil
	case errors.Is(err, flterrors.ErrResourceNameIsNil), errors.Is(err, flterrors.ErrIllegalResourceVersionFormat):
		return server.ReplaceDevice400JSONResponse(api.StatusBadRequest(err.Error())), nil
	case errors.Is(err, flterrors.ErrResourceNotFound):
		return server.ReplaceDevice404JSONResponse(api.StatusResourceNotFound("Device", request.Name)), nil
	case errors.Is(err, flterrors.ErrUpdatingResourceWithOwnerNotAllowed), errors.Is(err, flterrors.ErrNoRowsUpdated), errors.Is(err, flterrors.ErrResourceVersionConflict):
		return server.ReplaceDevice409JSONResponse(api.StatusResourceVersionConflict(err.Error())), nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/devices/{name})
func (h *ServiceHandler) DeleteDevice(ctx context.Context, request server.DeleteDeviceRequestObject) (server.DeleteDeviceResponseObject, error) {
	orgId := store.NullOrgId

	err := h.store.Device().Delete(ctx, orgId, request.Name, h.callbackManager.DeviceUpdatedCallback)
	switch {
	case err == nil:
		return server.DeleteDevice200JSONResponse{}, nil
	case errors.Is(err, flterrors.ErrResourceNotFound):
		return server.DeleteDevice404JSONResponse(api.StatusResourceNotFound("Device", request.Name)), nil
	default:
		return nil, err
	}
}

// (GET /api/v1/devices/{name}/status)
func (h *ServiceHandler) ReadDeviceStatus(ctx context.Context, request server.ReadDeviceStatusRequestObject) (server.ReadDeviceStatusResponseObject, error) {
	orgId := store.NullOrgId

	result, err := h.store.Device().Get(ctx, orgId, request.Name)
	switch {
	case err == nil:
		return server.ReadDeviceStatus200JSONResponse(*result), nil
	case errors.Is(err, flterrors.ErrResourceNotFound):
		return server.ReadDeviceStatus404JSONResponse(api.StatusResourceNotFound("Device", request.Name)), nil
	default:
		return nil, err
	}
}

// (PUT /api/v1/devices/{name}/status)
func (h *ServiceHandler) ReplaceDeviceStatus(ctx context.Context, request server.ReplaceDeviceStatusRequestObject) (server.ReplaceDeviceStatusResponseObject, error) {
	return common.ReplaceDeviceStatus(ctx, h.store, h.log, request)
}

// (GET /api/v1/devices/{name}/rendered)
func (h *ServiceHandler) GetRenderedDevice(ctx context.Context, request server.GetRenderedDeviceRequestObject) (server.GetRenderedDeviceResponseObject, error) {
	return common.GetRenderedDevice(ctx, h.store, h.log, request, h.agentEndpoint)
}

// (PATCH /api/v1/devices/{name})
// Only metadata.labels and spec can be patched. If we try to patch other fields, HTTP 400 Bad Request is returned.
func (h *ServiceHandler) PatchDevice(ctx context.Context, request server.PatchDeviceRequestObject) (server.PatchDeviceResponseObject, error) {
	orgId := store.NullOrgId

	currentObj, err := h.store.Device().Get(ctx, orgId, request.Name)
	if err != nil {
		switch {
		case errors.Is(err, flterrors.ErrResourceIsNil), errors.Is(err, flterrors.ErrResourceNameIsNil):
			return server.PatchDevice400JSONResponse(api.StatusBadRequest(err.Error())), nil
		case errors.Is(err, flterrors.ErrResourceNotFound):
			return server.PatchDevice404JSONResponse(api.StatusResourceNotFound("Device", request.Name)), nil
		default:
			return nil, err
		}
	}

	newObj := &api.Device{}
	err = ApplyJSONPatch(ctx, currentObj, newObj, *request.Body, "/api/v1/devices/"+request.Name)
	if err != nil {
		return server.PatchDevice400JSONResponse(api.StatusBadRequest(err.Error())), nil
	}

	if errs := newObj.Validate(); len(errs) > 0 {
		return server.PatchDevice400JSONResponse(api.StatusBadRequest(errors.Join(errs...).Error())), nil
	}
	if newObj.Metadata.Name == nil || *currentObj.Metadata.Name != *newObj.Metadata.Name {
		return server.PatchDevice400JSONResponse(api.StatusBadRequest("metadata.name is immutable")), nil
	}
	if currentObj.ApiVersion != newObj.ApiVersion {
		return server.PatchDevice400JSONResponse(api.StatusBadRequest("apiVersion is immutable")), nil
	}
	if currentObj.Kind != newObj.Kind {
		return server.PatchDevice400JSONResponse(api.StatusBadRequest("kind is immutable")), nil
	}
	if !reflect.DeepEqual(currentObj.Status, newObj.Status) {
		return server.PatchDevice400JSONResponse(api.StatusBadRequest("status is immutable")), nil
	}
	if newObj.Spec != nil && newObj.Spec.Decommissioning != nil {
		return server.PatchDevice400JSONResponse(api.StatusBadRequest("spec.decommissioning cannot be changed via patch request")), nil
	}

	common.NilOutManagedObjectMetaProperties(&newObj.Metadata)
	newObj.Metadata.ResourceVersion = nil

	var updateCallback func(uuid.UUID, *api.Device, *api.Device)

	if h.callbackManager != nil {
		updateCallback = h.callbackManager.DeviceUpdatedCallback
	}

	common.UpdateServiceSideStatus(ctx, h.store, h.log, orgId, newObj)

	// create
	result, err := h.store.Device().Update(ctx, orgId, newObj, nil, true, DeviceVerificationCallback, updateCallback)

	switch {
	case err == nil:
		return server.PatchDevice200JSONResponse(*result), nil
	case errors.Is(err, flterrors.ErrResourceIsNil), errors.Is(err, flterrors.ErrResourceNameIsNil), errors.Is(err, flterrors.ErrIllegalResourceVersionFormat):
		return server.PatchDevice400JSONResponse(api.StatusBadRequest(err.Error())), nil
	case errors.Is(err, flterrors.ErrResourceNotFound):
		return server.PatchDevice404JSONResponse(api.StatusResourceNotFound("Device", request.Name)), nil
	case errors.Is(err, flterrors.ErrNoRowsUpdated), errors.Is(err, flterrors.ErrResourceVersionConflict), errors.Is(err, flterrors.ErrUpdatingResourceWithOwnerNotAllowed):
		return server.PatchDevice409JSONResponse(api.StatusConflict(err.Error())), nil
	default:
		return nil, err
	}
}

// (PATCH /api/v1/devices/{name}/status)
func (h *ServiceHandler) PatchDeviceStatus(ctx context.Context, request server.PatchDeviceStatusRequestObject) (server.PatchDeviceStatusResponseObject, error) {
	return nil, fmt.Errorf("not yet implemented")
}

// (PUT /api/v1/devices/{name}/decommission)
func (h *ServiceHandler) DecommissionDevice(ctx context.Context, request server.DecommissionDeviceRequestObject) (server.DecommissionDeviceResponseObject, error) {
	orgId := store.NullOrgId

	deviceObj, err := h.store.Device().Get(ctx, orgId, request.Name)
	if err != nil {
		switch {
		case errors.Is(err, flterrors.ErrResourceIsNil), errors.Is(err, flterrors.ErrResourceNameIsNil):
			return server.DecommissionDevice400JSONResponse(api.StatusBadRequest(err.Error())), nil
		case errors.Is(err, flterrors.ErrResourceNotFound):
			return server.DecommissionDevice404JSONResponse(api.StatusResourceNotFound("Device", request.Name)), nil
		default:
			return nil, err
		}
	}
	if deviceObj.Spec != nil && deviceObj.Spec.Decommissioning != nil {
		return nil, fmt.Errorf("device already has decommissioning requested")
	}

	deviceObj.Status.Lifecycle.Status = api.DeviceLifecycleStatusDecommissioning
	deviceObj.Spec.Decommissioning = request.Body

	// these fields must be un-set so that device is no longer associated with any fleet
	deviceObj.Metadata.Owner = nil
	deviceObj.Metadata.Labels = nil

	var updateCallback func(uuid.UUID, *api.Device, *api.Device)

	if h.callbackManager != nil {
		updateCallback = h.callbackManager.DeviceUpdatedCallback
	}

	// set the fromAPI bool to 'false', otherwise updating the spec.decommissionRequested of a device is blocked
	result, err := h.store.Device().Update(ctx, orgId, deviceObj, []string{"status", "owner"}, false, DeviceVerificationCallback, updateCallback)

	switch {
	case err == nil:
		return server.DecommissionDevice200JSONResponse(*result), nil
	case errors.Is(err, flterrors.ErrResourceIsNil), errors.Is(err, flterrors.ErrResourceNameIsNil), errors.Is(err, flterrors.ErrIllegalResourceVersionFormat):
		return server.DecommissionDevice400JSONResponse(api.StatusBadRequest(err.Error())), nil
	case errors.Is(err, flterrors.ErrResourceNotFound):
		return server.DecommissionDevice404JSONResponse(api.StatusResourceNotFound("Device", request.Name)), nil
	default:
		return nil, err
	}
}
