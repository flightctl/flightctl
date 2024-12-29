package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/auth"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/go-openapi/swag"
)

// (POST /api/v1/devices)
func (h *ServiceHandler) CreateDevice(ctx context.Context, request server.CreateDeviceRequestObject) (server.CreateDeviceResponseObject, error) {
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "devices", "create")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.CreateDevice503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.CreateDevice403JSONResponse{Message: Forbidden}, nil
	}

	orgId := store.NullOrgId

	// don't set fields that are managed by the service
	request.Body.Status = nil
	common.NilOutManagedObjectMetaProperties(&request.Body.Metadata)

	if errs := request.Body.Validate(); len(errs) > 0 {
		return server.CreateDevice400JSONResponse{Message: errors.Join(errs...).Error()}, nil
	}

	common.UpdateServiceSideStatus(ctx, h.store, h.log, orgId, request.Body)

	result, err := h.store.Device().Create(ctx, orgId, request.Body, h.callbackManager.DeviceUpdatedCallback)
	switch err {
	case nil:
		return server.CreateDevice201JSONResponse(*result), nil
	case flterrors.ErrResourceIsNil, flterrors.ErrIllegalResourceVersionFormat:
		return server.CreateDevice400JSONResponse{Message: err.Error()}, nil
	case flterrors.ErrDuplicateName:
		return server.CreateDevice409JSONResponse{Message: err.Error()}, nil
	default:
		return nil, err
	}
}

// (GET /api/v1/devices)
func (h *ServiceHandler) ListDevices(ctx context.Context, request server.ListDevicesRequestObject) (server.ListDevicesResponseObject, error) {
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "devices", "list")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.ListDevices503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.ListDevices403JSONResponse{Message: Forbidden}, nil
	}

	orgId := store.NullOrgId

	statusFilter := []string{}
	if request.Params.StatusFilter != nil {
		for _, filter := range *request.Params.StatusFilter {
			statusFilter = append(statusFilter, fmt.Sprintf("status.%s", filter))
		}
	}

	filterMap, err := ConvertFieldFilterParamsToMap(statusFilter)
	if err != nil {
		return server.ListDevices400JSONResponse{Message: fmt.Sprintf("failed to convert status filter: %v", err)}, nil
	}

	var fieldSelector *selector.FieldSelector
	if request.Params.FieldSelector != nil {
		if fieldSelector, err = selector.NewFieldSelector(*request.Params.FieldSelector); err != nil {
			return server.ListDevices400JSONResponse{Message: fmt.Sprintf("failed to parse field selector: %v", err)}, nil
		}
	}

	var labelSelector *selector.LabelSelector
	if request.Params.LabelSelector != nil {
		if labelSelector, err = selector.NewLabelSelector(*request.Params.LabelSelector); err != nil {
			return server.ListDevices400JSONResponse{Message: fmt.Sprintf("failed to parse label selector: %v", err)}, nil
		}
	}

	// Check if SummaryOnly is true
	if request.Params.SummaryOnly != nil && *request.Params.SummaryOnly {
		// Check for unsupported parameters
		if request.Params.Limit != nil ||
			request.Params.Continue != nil {
			return server.ListDevices400JSONResponse{
				Message: "parameters such as 'limit', and 'continue' are not supported when 'summaryOnly' is true",
			}, nil
		}

		result, err := h.store.Device().Summary(ctx, orgId, store.ListParams{
			Filter:        filterMap,
			Owners:        util.OwnerQueryParamsToArray(request.Params.Owner),
			FieldSelector: fieldSelector,
			LabelSelector: labelSelector,
		})

		switch err {
		case nil:
			// Create an empty DeviceList and set the summary
			emptyList := model.DeviceList.ToApiResource(nil, nil, nil)
			emptyList.Summary = result
			return server.ListDevices200JSONResponse(emptyList), nil
		default:
			return nil, err
		}
	}

	cont, err := store.ParseContinueString(request.Params.Continue)
	if err != nil {
		return server.ListDevices400JSONResponse{Message: fmt.Sprintf("failed to parse continue parameter: %v", err)}, nil
	}

	listParams := store.ListParams{
		Filter:        filterMap,
		Limit:         int(swag.Int32Value(request.Params.Limit)),
		Continue:      cont,
		Owners:        util.OwnerQueryParamsToArray(request.Params.Owner),
		FieldSelector: fieldSelector,
		LabelSelector: labelSelector,
	}
	if listParams.Limit == 0 {
		listParams.Limit = store.MaxRecordsPerListRequest
	}
	if listParams.Limit > store.MaxRecordsPerListRequest {
		return server.ListDevices400JSONResponse{Message: fmt.Sprintf("limit cannot exceed %d", store.MaxRecordsPerListRequest)}, nil
	}

	result, err := h.store.Device().List(ctx, orgId, listParams)
	if err == nil {
		return server.ListDevices200JSONResponse(*result), nil
	}

	var se *selector.SelectorError

	switch {
	case errors.Is(err, flterrors.ErrLimitParamOutOfBounds):
		return server.ListDevices400JSONResponse{Message: err.Error()}, nil
	case selector.AsSelectorError(err, &se):
		return server.ListDevices400JSONResponse{Message: se.Error()}, nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/devices)
func (h *ServiceHandler) DeleteDevices(ctx context.Context, request server.DeleteDevicesRequestObject) (server.DeleteDevicesResponseObject, error) {
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "devices", "deletecollection")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.DeleteDevices503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.DeleteDevices403JSONResponse{Message: Forbidden}, nil
	}
	orgId := store.NullOrgId

	err = h.store.Device().DeleteAll(ctx, orgId, h.callbackManager.AllDevicesDeletedCallback)
	switch err {
	case nil:
		return server.DeleteDevices200JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (GET /api/v1/devices/{name})
func (h *ServiceHandler) ReadDevice(ctx context.Context, request server.ReadDeviceRequestObject) (server.ReadDeviceResponseObject, error) {
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "devices", "get")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.ReadDevice503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.ReadDevice403JSONResponse{Message: Forbidden}, nil
	}
	orgId := store.NullOrgId

	result, err := h.store.Device().Get(ctx, orgId, request.Name)
	switch err {
	case nil:
		return server.ReadDevice200JSONResponse(*result), nil
	case flterrors.ErrResourceNotFound:
		return server.ReadDevice404JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (PUT /api/v1/devices/{name})
func (h *ServiceHandler) ReplaceDevice(ctx context.Context, request server.ReplaceDeviceRequestObject) (server.ReplaceDeviceResponseObject, error) {
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "devices", "update")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.ReplaceDevice503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.ReplaceDevice403JSONResponse{Message: Forbidden}, nil
	}
	orgId := store.NullOrgId

	// don't overwrite fields that are managed by the service
	request.Body.Status = nil
	common.NilOutManagedObjectMetaProperties(&request.Body.Metadata)

	if errs := request.Body.Validate(); len(errs) > 0 {
		return server.ReplaceDevice400JSONResponse{Message: errors.Join(errs...).Error()}, nil
	}
	if request.Name != *request.Body.Metadata.Name {
		return server.ReplaceDevice400JSONResponse{Message: "resource name specified in metadata does not match name in path"}, nil
	}

	common.UpdateServiceSideStatus(ctx, h.store, h.log, orgId, request.Body)

	result, created, err := h.store.Device().CreateOrUpdate(ctx, orgId, request.Body, nil, true, h.callbackManager.DeviceUpdatedCallback)
	switch err {
	case nil:
		if created {
			return server.ReplaceDevice201JSONResponse(*result), nil
		} else {
			return server.ReplaceDevice200JSONResponse(*result), nil
		}
	case flterrors.ErrResourceIsNil:
		return server.ReplaceDevice400JSONResponse{Message: err.Error()}, nil
	case flterrors.ErrResourceNameIsNil, flterrors.ErrIllegalResourceVersionFormat:
		return server.ReplaceDevice400JSONResponse{Message: err.Error()}, nil
	case flterrors.ErrResourceNotFound:
		return server.ReplaceDevice404JSONResponse{}, nil
	case flterrors.ErrUpdatingResourceWithOwnerNotAllowed, flterrors.ErrNoRowsUpdated, flterrors.ErrResourceVersionConflict:
		return server.ReplaceDevice409JSONResponse{Message: err.Error()}, nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/devices/{name})
func (h *ServiceHandler) DeleteDevice(ctx context.Context, request server.DeleteDeviceRequestObject) (server.DeleteDeviceResponseObject, error) {
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "devices", "delete")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.DeleteDevice503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.DeleteDevice403JSONResponse{Message: Forbidden}, nil
	}
	orgId := store.NullOrgId

	err = h.store.Device().Delete(ctx, orgId, request.Name, h.callbackManager.DeviceUpdatedCallback)
	switch err {
	case nil:
		return server.DeleteDevice200JSONResponse{}, nil
	case flterrors.ErrResourceNotFound:
		return server.DeleteDevice404JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (GET /api/v1/devices/{name}/status)
func (h *ServiceHandler) ReadDeviceStatus(ctx context.Context, request server.ReadDeviceStatusRequestObject) (server.ReadDeviceStatusResponseObject, error) {
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "devices/status", "get")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.ReadDeviceStatus503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.ReadDeviceStatus403JSONResponse{Message: Forbidden}, nil
	}
	orgId := store.NullOrgId

	result, err := h.store.Device().Get(ctx, orgId, request.Name)
	switch err {
	case nil:
		return server.ReadDeviceStatus200JSONResponse(*result), nil
	case flterrors.ErrResourceNotFound:
		return server.ReadDeviceStatus404JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (PUT /api/v1/devices/{name}/status)
func (h *ServiceHandler) ReplaceDeviceStatus(ctx context.Context, request server.ReplaceDeviceStatusRequestObject) (server.ReplaceDeviceStatusResponseObject, error) {
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "devices/status", "update")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.ReplaceDeviceStatus503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.ReplaceDeviceStatus403JSONResponse{Message: Forbidden}, nil
	}
	return common.ReplaceDeviceStatus(ctx, h.store, h.log, request)
}

// (GET /api/v1/devices/{name}/rendered)
func (h *ServiceHandler) GetRenderedDeviceSpec(ctx context.Context, request server.GetRenderedDeviceSpecRequestObject) (server.GetRenderedDeviceSpecResponseObject, error) {
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "devices/rendered", "get")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.GetRenderedDeviceSpec503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.GetRenderedDeviceSpec403JSONResponse{Message: Forbidden}, nil
	}
	return common.GetRenderedDeviceSpec(ctx, h.store, h.log, request, h.agentEndpoint)
}

// (PATCH /api/v1/devices/{name})
// Only metadata.labels and spec can be patched. If we try to patch other fields, HTTP 400 Bad Request is returned.
func (h *ServiceHandler) PatchDevice(ctx context.Context, request server.PatchDeviceRequestObject) (server.PatchDeviceResponseObject, error) {
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "devices", "patch")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.PatchDevice503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.PatchDevice403JSONResponse{Message: Forbidden}, nil
	}
	orgId := store.NullOrgId

	currentObj, err := h.store.Device().Get(ctx, orgId, request.Name)
	if err != nil {
		switch err {
		case flterrors.ErrResourceIsNil, flterrors.ErrResourceNameIsNil:
			return server.PatchDevice400JSONResponse{Message: err.Error()}, nil
		case flterrors.ErrResourceNotFound:
			return server.PatchDevice404JSONResponse{}, nil
		default:
			return nil, err
		}
	}

	newObj := &v1alpha1.Device{}
	err = ApplyJSONPatch(ctx, currentObj, newObj, *request.Body, "/api/v1/devices/"+request.Name)
	if err != nil {
		return server.PatchDevice400JSONResponse{Message: err.Error()}, nil
	}

	if errs := newObj.Validate(); len(errs) > 0 {
		return server.PatchDevice400JSONResponse{Message: errors.Join(errs...).Error()}, nil
	}
	if newObj.Metadata.Name == nil || *currentObj.Metadata.Name != *newObj.Metadata.Name {
		return server.PatchDevice400JSONResponse{Message: "metadata.name is immutable"}, nil
	}
	if currentObj.ApiVersion != newObj.ApiVersion {
		return server.PatchDevice400JSONResponse{Message: "apiVersion is immutable"}, nil
	}
	if currentObj.Kind != newObj.Kind {
		return server.PatchDevice400JSONResponse{Message: "kind is immutable"}, nil
	}
	if !reflect.DeepEqual(currentObj.Status, newObj.Status) {
		return server.PatchDevice400JSONResponse{Message: "status is immutable"}, nil
	}

	common.NilOutManagedObjectMetaProperties(&newObj.Metadata)
	newObj.Metadata.ResourceVersion = nil

	var updateCallback func(before *model.Device, after *model.Device)

	if h.callbackManager != nil {
		updateCallback = h.callbackManager.DeviceUpdatedCallback
	}

	common.UpdateServiceSideStatus(ctx, h.store, h.log, orgId, newObj)

	// create
	result, err := h.store.Device().Update(ctx, orgId, newObj, nil, true, updateCallback)

	switch err {
	case nil:
		return server.PatchDevice200JSONResponse(*result), nil
	case flterrors.ErrResourceIsNil, flterrors.ErrResourceNameIsNil, flterrors.ErrIllegalResourceVersionFormat:
		return server.PatchDevice400JSONResponse{Message: err.Error()}, nil
	case flterrors.ErrResourceNotFound:
		return server.PatchDevice404JSONResponse{}, nil
	case flterrors.ErrNoRowsUpdated, flterrors.ErrResourceVersionConflict, flterrors.ErrUpdatingResourceWithOwnerNotAllowed:
		return server.PatchDevice409JSONResponse{}, nil
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
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "devices/decommission", "update")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.DecommissionDevice503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.DecommissionDevice403JSONResponse{Message: Forbidden}, nil
	}
	return nil, fmt.Errorf("not yet implemented")
}
