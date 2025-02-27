package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/util/validation"
	"github.com/go-openapi/swag"
	"github.com/google/uuid"
)

func (h *ServiceHandler) CreateDevice(ctx context.Context, device api.Device) (*api.Device, api.Status) {
	if device.Spec != nil && device.Spec.Decommissioning != nil {
		h.log.WithError(flterrors.ErrDecommission).Error("attempt to create decommissioned device")
		return nil, api.StatusBadRequest(flterrors.ErrDecommission.Error())
	}

	orgId := store.NullOrgId

	// don't set fields that are managed by the service
	device.Status = nil
	NilOutManagedObjectMetaProperties(&device.Metadata)

	if errs := device.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}

	common.UpdateServiceSideStatus(ctx, h.store, h.log, orgId, &device)

	result, err := h.store.Device().Create(ctx, orgId, &device, h.callbackManager.DeviceUpdatedCallback)
	return result, StoreErrorToApiStatus(err, true, api.DeviceKind, device.Metadata.Name)
}

func (h *ServiceHandler) ListDevices(ctx context.Context, params api.ListDevicesParams) (*api.DeviceList, api.Status) {
	orgId := store.NullOrgId

	var (
		fieldSelector *selector.FieldSelector
		err           error
	)

	if params.FieldSelector != nil {
		if fieldSelector, err = selector.NewFieldSelector(*params.FieldSelector); err != nil {
			return nil, api.StatusBadRequest(fmt.Sprintf("failed to parse field selector: %v", err))
		}
	}

	var labelSelector *selector.LabelSelector
	if params.LabelSelector != nil {
		if labelSelector, err = selector.NewLabelSelector(*params.LabelSelector); err != nil {
			return nil, api.StatusBadRequest(fmt.Sprintf("failed to parse label selector: %v", err))
		}
	}

	// Check if SummaryOnly is true
	if params.SummaryOnly != nil && *params.SummaryOnly {
		// Check for unsupported parameters
		if params.Limit != nil || params.Continue != nil {
			return nil, api.StatusBadRequest("parameters such as 'limit', and 'continue' are not supported when 'summaryOnly' is true")
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
			return &emptyList, api.StatusOK()
		default:
			return nil, api.StatusInternalServerError(err.Error())
		}
	}

	cont, err := store.ParseContinueString(params.Continue)
	if err != nil {
		return nil, api.StatusBadRequest(fmt.Sprintf("failed to parse continue parameter: %v", err))
	}

	listParams := store.ListParams{
		Limit:         int(swag.Int32Value(params.Limit)),
		Continue:      cont,
		FieldSelector: fieldSelector,
		LabelSelector: labelSelector,
	}
	if listParams.Limit == 0 {
		listParams.Limit = store.MaxRecordsPerListRequest
	} else if listParams.Limit > store.MaxRecordsPerListRequest {
		return nil, api.StatusBadRequest(fmt.Sprintf("limit cannot exceed %d", store.MaxRecordsPerListRequest))
	} else if listParams.Limit < 0 {
		return nil, api.StatusBadRequest("limit cannot be negative")
	}

	result, err := h.store.Device().List(ctx, orgId, listParams)
	if err == nil {
		return result, api.StatusOK()
	}

	var se *selector.SelectorError

	switch {
	case selector.AsSelectorError(err, &se):
		return nil, api.StatusBadRequest(se.Error())
	default:
		return nil, api.StatusInternalServerError(err.Error())
	}
}

func (h *ServiceHandler) DeleteDevices(ctx context.Context) api.Status {
	orgId := store.NullOrgId

	err := h.store.Device().DeleteAll(ctx, orgId, h.callbackManager.AllDevicesDeletedCallback)
	return StoreErrorToApiStatus(err, false, api.DeviceKind, nil)
}

func (h *ServiceHandler) GetDevice(ctx context.Context, name string) (*api.Device, api.Status) {
	orgId := store.NullOrgId

	result, err := h.store.Device().Get(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
}

func DeviceVerificationCallback(before, after *api.Device) error {
	// Ensure the device wasn't decommissioned
	if before != nil && before.Spec != nil && before.Spec.Decommissioning != nil {
		return flterrors.ErrDecommission
	}
	return nil
}

func (h *ServiceHandler) ReplaceDevice(ctx context.Context, name string, device api.Device) (*api.Device, api.Status) {
	if device.Spec != nil && device.Spec.Decommissioning != nil {
		h.log.WithError(flterrors.ErrDecommission).Error("attempt to set decommissioned status when replacing device, or to replace decommissioned device")
		return nil, api.StatusBadRequest(flterrors.ErrDecommission.Error())
	}

	orgId := store.NullOrgId

	// don't overwrite fields that are managed by the service
	device.Status = nil
	NilOutManagedObjectMetaProperties(&device.Metadata)

	if errs := device.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}
	if name != *device.Metadata.Name {
		return nil, api.StatusBadRequest("resource name specified in metadata does not match name in path")
	}

	common.UpdateServiceSideStatus(ctx, h.store, h.log, orgId, &device)

	result, created, err := h.store.Device().CreateOrUpdate(ctx, orgId, &device, nil, true, DeviceVerificationCallback, h.callbackManager.DeviceUpdatedCallback)
	return result, StoreErrorToApiStatus(err, created, api.DeviceKind, &name)
}

func (h *ServiceHandler) DeleteDevice(ctx context.Context, name string) api.Status {
	orgId := store.NullOrgId

	err := h.store.Device().Delete(ctx, orgId, name, h.callbackManager.DeviceUpdatedCallback)
	return StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
}

// (GET /api/v1/devices/{name}/status)
func (h *ServiceHandler) GetDeviceStatus(ctx context.Context, name string) (*api.Device, api.Status) {
	orgId := store.NullOrgId

	result, err := h.store.Device().Get(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
}

func validateDeviceStatus(d *api.Device) []error {
	allErrs := []error{}
	allErrs = append(allErrs, validation.ValidateResourceName(d.Metadata.Name)...)
	// TODO: implement validation of agent's status updates
	return allErrs
}

func (h *ServiceHandler) ReplaceDeviceStatus(ctx context.Context, name string, device api.Device) (*api.Device, api.Status) {
	orgId := store.NullOrgId

	if errs := validateDeviceStatus(&device); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}
	if name != *device.Metadata.Name {
		return nil, api.StatusBadRequest("resource name specified in metadata does not match name in path")
	}
	device.Status.LastSeen = time.Now()

	// UpdateServiceSideStatus() needs to know the latest .metadata.annotations[device-controller/renderedVersion]
	// that the agent does not provide or only have an outdated knowledge of
	oldDevice, err := h.store.Device().Get(ctx, orgId, name)
	if err != nil {
		return nil, StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
	}
	// do not overwrite valid service-side lifecycle status with placeholder device-side status
	if device.Status.Lifecycle.Status == api.DeviceLifecycleStatusUnknown {
		device.Status.Lifecycle.Status = oldDevice.Status.Lifecycle.Status
	}
	oldDevice.Status = device.Status
	common.UpdateServiceSideStatus(ctx, h.store, h.log, orgId, oldDevice)

	result, err := h.store.Device().UpdateStatus(ctx, orgId, oldDevice)
	return result, StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
}

func (h *ServiceHandler) GetRenderedDevice(ctx context.Context, name string, params api.GetRenderedDeviceParams) (*api.Device, api.Status) {
	orgId := store.NullOrgId

	result, err := h.store.Device().GetRendered(ctx, orgId, name, params.KnownRenderedVersion, h.agentEndpoint)
	if err == nil && result == nil {
		return nil, api.StatusNoContent()
	}
	return result, StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
}

// Only metadata.labels and spec can be patched. If we try to patch other fields, HTTP 400 Bad Request is returned.
func (h *ServiceHandler) PatchDevice(ctx context.Context, name string, patch api.PatchRequest) (*api.Device, api.Status) {
	orgId := store.NullOrgId

	currentObj, err := h.store.Device().Get(ctx, orgId, name)
	if err != nil {
		return nil, StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
	}

	newObj := &api.Device{}
	err = ApplyJSONPatch(ctx, currentObj, newObj, patch, "/api/v1/devices/"+name)
	if err != nil {
		return nil, api.StatusBadRequest(err.Error())
	}

	if errs := newObj.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}
	if newObj.Metadata.Name == nil || *currentObj.Metadata.Name != *newObj.Metadata.Name {
		return nil, api.StatusBadRequest("metadata.name is immutable")
	}
	if currentObj.ApiVersion != newObj.ApiVersion {
		return nil, api.StatusBadRequest("apiVersion is immutable")
	}
	if currentObj.Kind != newObj.Kind {
		return nil, api.StatusBadRequest("kind is immutable")
	}
	if !reflect.DeepEqual(currentObj.Status, newObj.Status) {
		return nil, api.StatusBadRequest("status is immutable")
	}
	if newObj.Spec != nil && newObj.Spec.Decommissioning != nil {
		return nil, api.StatusBadRequest("spec.decommissioning cannot be changed via patch request")
	}

	NilOutManagedObjectMetaProperties(&newObj.Metadata)
	newObj.Metadata.ResourceVersion = nil

	var updateCallback func(uuid.UUID, *api.Device, *api.Device)

	if h.callbackManager != nil {
		updateCallback = h.callbackManager.DeviceUpdatedCallback
	}

	common.UpdateServiceSideStatus(ctx, h.store, h.log, orgId, newObj)

	result, err := h.store.Device().Update(ctx, orgId, newObj, nil, true, DeviceVerificationCallback, updateCallback)
	return result, StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
}

func (h *ServiceHandler) DecommissionDevice(ctx context.Context, name string, decom api.DeviceDecommission) (*api.Device, api.Status) {
	orgId := store.NullOrgId

	deviceObj, err := h.store.Device().Get(ctx, orgId, name)
	if err != nil {
		return nil, StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
	}
	if deviceObj.Spec != nil && deviceObj.Spec.Decommissioning != nil {
		return nil, api.StatusConflict("device already has decommissioning requested")
	}

	deviceObj.Status.Lifecycle.Status = api.DeviceLifecycleStatusDecommissioning
	deviceObj.Spec.Decommissioning = &decom

	// these fields must be un-set so that device is no longer associated with any fleet
	deviceObj.Metadata.Owner = nil
	deviceObj.Metadata.Labels = nil

	var updateCallback func(uuid.UUID, *api.Device, *api.Device)

	if h.callbackManager != nil {
		updateCallback = h.callbackManager.DeviceUpdatedCallback
	}

	// set the fromAPI bool to 'false', otherwise updating the spec.decommissionRequested of a device is blocked
	result, err := h.store.Device().Update(ctx, orgId, deviceObj, []string{"status", "owner"}, false, DeviceVerificationCallback, updateCallback)
	return result, StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
}
