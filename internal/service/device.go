package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/auth"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/util"
	k8sselector "github.com/flightctl/flightctl/pkg/k8s/selector"
	"github.com/flightctl/flightctl/pkg/k8s/selector/fields"
	"github.com/go-openapi/swag"
	"k8s.io/apimachinery/pkg/labels"
)

// (POST /api/v1/devices)
func (h *ServiceHandler) CreateDevice(ctx context.Context, request server.CreateDeviceRequestObject) (server.CreateDeviceResponseObject, error) {
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
	case flterrors.ErrResourceIsNil:
		return server.CreateDevice400JSONResponse{Message: err.Error()}, nil
	default:
		return nil, err
	}
}

// (GET /api/v1/devices)
func (h *ServiceHandler) ListDevices(ctx context.Context, request server.ListDevicesRequestObject) (server.ListDevicesResponseObject, error) {
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "devices", "list")
	if err != nil {
		return server.ListDevices401JSONResponse{Message: fmt.Sprintf("auth failed: %v", err)}, nil
	}
	if !allowed {
		return server.ListDevices403JSONResponse{Message: "cannot list devices"}, nil
	}

	orgId := store.NullOrgId

	labelSelector := ""
	if request.Params.LabelSelector != nil {
		// If a label selector is provided, ensure keys without value still have '=' appended
		labels := strings.Split(*request.Params.LabelSelector, ",")
		for i, label := range labels {
			l := strings.Split(label, "=")
			if len(l) == 1 {
				labels[i] = l[0] + "="
			}
		}
		labelSelector = strings.Join(labels, ",")
	}

	labelMap, err := labels.ConvertSelectorToLabelsMap(labelSelector)
	if err != nil {
		return server.ListDevices400JSONResponse{Message: err.Error()}, nil
	}

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

	var fieldSelector k8sselector.Selector
	if request.Params.FieldSelector != nil {
		if fieldSelector, err = fields.ParseSelector(*request.Params.FieldSelector); err != nil {
			return server.ListDevices400JSONResponse{Message: fmt.Sprintf("failed to parse field selector: %v", err)}, nil
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
			Labels:        labelMap,
			Filter:        filterMap,
			Owners:        util.OwnerQueryParamsToArray(request.Params.Owner),
			FieldSelector: fieldSelector,
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
		Labels:        labelMap,
		Filter:        filterMap,
		Limit:         int(swag.Int32Value(request.Params.Limit)),
		Continue:      cont,
		Owners:        util.OwnerQueryParamsToArray(request.Params.Owner),
		FieldSelector: fieldSelector,
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
	orgId := store.NullOrgId

	err := h.store.Device().DeleteAll(ctx, orgId, h.callbackManager.AllDevicesDeletedCallback)
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
	case flterrors.ErrResourceNotFound:
		return server.ReadDevice404JSONResponse{}, nil
	default:
		return nil, err
	}
}

// (PUT /api/v1/devices/{name})
func (h *ServiceHandler) ReplaceDevice(ctx context.Context, request server.ReplaceDeviceRequestObject) (server.ReplaceDeviceResponseObject, error) {
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
	orgId := store.NullOrgId

	err := h.store.Device().Delete(ctx, orgId, request.Name, h.callbackManager.DeviceUpdatedCallback)
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
	return common.ReplaceDeviceStatus(ctx, h.store, h.log, request)
}

// (GET /api/v1/devices/{name}/rendered)
func (h *ServiceHandler) GetRenderedDeviceSpec(ctx context.Context, request server.GetRenderedDeviceSpecRequestObject) (server.GetRenderedDeviceSpecResponseObject, error) {
	return common.GetRenderedDeviceSpec(ctx, h.store, h.log, request, h.consoleGrpcEndpoint)
}

// (PATCH /api/v1/devices/{name})
// Only metadata.labels and spec can be patched. If we try to patch other fields, HTTP 400 Bad Request is returned.
func (h *ServiceHandler) PatchDevice(ctx context.Context, request server.PatchDeviceRequestObject) (server.PatchDeviceResponseObject, error) {
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

// (PUT /api/v1/devices/{name}/decommission)
func (h *ServiceHandler) DecommissionDevice(ctx context.Context, request server.DecommissionDeviceRequestObject) (server.DecommissionDeviceResponseObject, error) {
	return nil, fmt.Errorf("not yet implemented")
}
