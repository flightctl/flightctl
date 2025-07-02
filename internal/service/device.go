package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/util/validation"
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

	resourceEventFromUpdateDetailsFunc := func(ctx context.Context, update common.ResourceUpdate) *api.Event {
		return GetResourceEventFromUpdateDetails(ctx, api.DeviceKind, *device.Metadata.Name, update.Reason, update.UpdateDetails, h.log)
	}
	common.UpdateServiceSideStatus(ctx, orgId, &device, nil, h.store, h.log, h.CreateEvent, resourceEventFromUpdateDetailsFunc)

	result, err := h.store.Device().Create(ctx, orgId, &device, h.callbackManager.DeviceUpdatedCallback)
	status := StoreErrorToApiStatus(err, true, api.DeviceKind, device.Metadata.Name)
	if err == nil {
		h.CreateEvent(ctx, GetResourceCreatedOrUpdatedEvent(ctx, true, api.DeviceKind, *device.Metadata.Name, status, nil, h.log))
	}

	return result, status
}

func convertDeviceListParams(params api.ListDevicesParams, annotationSelector *selector.AnnotationSelector) (*store.ListParams, api.Status) {
	listParams, status := prepareListParams(params.Continue, params.LabelSelector, params.FieldSelector, params.Limit)
	if status != api.StatusOK() {
		return nil, status
	}
	listParams.AnnotationSelector = annotationSelector
	return listParams, api.StatusOK()
}

func (h *ServiceHandler) ListDevices(ctx context.Context, params api.ListDevicesParams, annotationSelector *selector.AnnotationSelector) (*api.DeviceList, api.Status) {
	orgId := store.NullOrgId
	storeParams, status := convertDeviceListParams(params, annotationSelector)
	if status.Code != http.StatusOK {
		return nil, status
	}

	// Check if SummaryOnly is true
	if params.SummaryOnly != nil && *params.SummaryOnly {
		// Check for unsupported parameters
		if params.Limit != nil || params.Continue != nil {
			return nil, api.StatusBadRequest("parameters such as 'limit', and 'continue' are not supported when 'summaryOnly' is true")
		}

		result, err := h.store.Device().Summary(ctx, orgId, *storeParams)

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

	if storeParams.Limit == 0 {
		storeParams.Limit = MaxRecordsPerListRequest
	} else if storeParams.Limit > MaxRecordsPerListRequest {
		return nil, api.StatusBadRequest(fmt.Sprintf("limit cannot exceed %d", MaxRecordsPerListRequest))
	} else if storeParams.Limit < 0 {
		return nil, api.StatusBadRequest("limit cannot be negative")
	}

	result, err := h.store.Device().List(ctx, orgId, *storeParams)
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

func (h *ServiceHandler) GetDevice(ctx context.Context, name string) (*api.Device, api.Status) {
	orgId := store.NullOrgId

	result, err := h.store.Device().Get(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
}

func DeviceVerificationCallback(ctx context.Context, before, after *api.Device) error {
	// Ensure the device wasn't decommissioned
	if before != nil && before.Spec != nil && before.Spec.Decommissioning != nil {
		return flterrors.ErrDecommission
	}
	return nil
}

func (h *ServiceHandler) ReplaceDevice(ctx context.Context, name string, device api.Device, fieldsToUnset []string) (*api.Device, api.Status) {
	if device.Spec != nil && device.Spec.Decommissioning != nil {
		h.log.WithError(flterrors.ErrDecommission).Error("attempt to set decommissioned status when replacing device, or to replace decommissioned device")
		return nil, api.StatusBadRequest(flterrors.ErrDecommission.Error())
	}

	orgId := store.NullOrgId

	// don't overwrite fields that are managed by the service for external requests
	isNotInternal := !IsInternalRequest(ctx)
	if isNotInternal {
		device.Status = nil
		NilOutManagedObjectMetaProperties(&device.Metadata)
	}

	if errs := device.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}
	if name != *device.Metadata.Name {
		return nil, api.StatusBadRequest("resource name specified in metadata does not match name in path")
	}

	var callback store.DeviceStoreCallback = h.callbackManager.DeviceUpdatedCallback
	delayDeviceRender, ok := ctx.Value(consts.DelayDeviceRenderCtxKey).(bool)
	if ok && delayDeviceRender {
		callback = h.callbackManager.DeviceUpdatedNoRenderCallback
	}

	oldDevice, err := h.store.Device().Get(ctx, orgId, name)
	if err != nil {
		oldDevice = nil
	}
	resourceEventFromUpdateDetailsFunc := func(ctx context.Context, update common.ResourceUpdate) *api.Event {
		return GetResourceEventFromUpdateDetails(ctx, api.DeviceKind, *device.Metadata.Name, update.Reason, update.UpdateDetails, h.log)
	}
	updated := common.UpdateServiceSideStatus(ctx, orgId, &device, oldDevice, h.store, h.log, h.CreateEvent, resourceEventFromUpdateDetailsFunc)

	result, created, updateDesc, err := h.store.Device().CreateOrUpdate(ctx, orgId, &device, fieldsToUnset, isNotInternal, DeviceVerificationCallback, callback)
	status := StoreErrorToApiStatus(err, created, api.DeviceKind, &name)
	if updated && err == nil {
		h.CreateEvent(ctx, GetResourceCreatedOrUpdatedEvent(ctx, created, api.DeviceKind, name, status, &updateDesc, h.log))
	}
	return result, status
}

func (h *ServiceHandler) UpdateDevice(ctx context.Context, name string, device api.Device, fieldsToUnset []string) (*api.Device, error) {
	if device.Spec != nil && device.Spec.Decommissioning != nil {
		h.log.WithError(flterrors.ErrDecommission).Error("attempt to set decommissioned status when replacing device, or to replace decommissioned device")
		return nil, flterrors.ErrDecommission
	}

	orgId := store.NullOrgId

	// don't overwrite fields that are managed by the service for external requests
	if !IsInternalRequest(ctx) {
		device.Status = nil
		NilOutManagedObjectMetaProperties(&device.Metadata)
	}

	if errs := device.Validate(); len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if name != *device.Metadata.Name {
		return nil, fmt.Errorf("resource name specified in metadata does not match name in path")
	}

	var callback store.DeviceStoreCallback = h.callbackManager.DeviceUpdatedCallback
	delayDeviceRender, ok := ctx.Value(consts.DelayDeviceRenderCtxKey).(bool)
	if ok && delayDeviceRender {
		callback = h.callbackManager.DeviceUpdatedNoRenderCallback
	}
	oldDevice := device
	resourceEventFromUpdateDetailsFunc := func(ctx context.Context, update common.ResourceUpdate) *api.Event {
		return GetResourceEventFromUpdateDetails(ctx, api.DeviceKind, *device.Metadata.Name, update.Reason, update.UpdateDetails, h.log)
	}
	updated := common.UpdateServiceSideStatus(ctx, orgId, &device, &oldDevice, h.store, h.log, h.CreateEvent, resourceEventFromUpdateDetailsFunc)

	dev, updateDesc, err := h.store.Device().Update(ctx, orgId, &device, fieldsToUnset, false, DeviceVerificationCallback, callback)
	status := StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
	if updated && err == nil {
		h.CreateEvent(ctx, GetResourceCreatedOrUpdatedEvent(ctx, false, api.DeviceKind, name, status, &updateDesc, h.log))
	}
	return dev, err
}

func (h *ServiceHandler) DeleteDevice(ctx context.Context, name string) api.Status {
	orgId := store.NullOrgId

	deleted, err := h.store.Device().Delete(ctx, orgId, name, h.callbackManager.DeviceUpdatedCallback)
	status := StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
	if deleted || err != nil {
		h.CreateEvent(ctx, GetResourceDeletedEvent(ctx, api.DeviceKind, name, status, h.log))
	}
	return status
}

// (GET /api/v1/devices/{name}/status)
func (h *ServiceHandler) GetDeviceStatus(ctx context.Context, name string) (*api.Device, api.Status) {
	orgId := store.NullOrgId

	result, err := h.store.Device().Get(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
}

func validateDeviceStatus(d *api.Device) []error {
	allErrs := append([]error{}, validation.ValidateResourceName(d.Metadata.Name)...)
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
	common.KeepDBDeviceStatus(&device, oldDevice)
	oldDevice.Status = device.Status
	resourceEventFromUpdateDetailsFunc := func(ctx context.Context, update common.ResourceUpdate) *api.Event {
		return GetResourceEventFromUpdateDetails(ctx, api.DeviceKind, *device.Metadata.Name, update.Reason, update.UpdateDetails, h.log)
	}
	updated := common.UpdateServiceSideStatus(ctx, orgId, oldDevice, oldDevice, h.store, h.log, h.CreateEvent, resourceEventFromUpdateDetailsFunc)

	result, err := h.store.Device().UpdateStatus(ctx, orgId, oldDevice)
	status := StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
	if updated && err != nil {
		h.CreateEvent(ctx, GetResourceCreatedOrUpdatedEvent(ctx, false, api.DeviceKind, name, status, nil, h.log))
	}
	return result, status
}

func (h *ServiceHandler) PatchDeviceStatus(ctx context.Context, name string, patch api.PatchRequest) (*api.Device, api.Status) {
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

	if errs := validateDeviceStatus(newObj); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}

	if errs := newObj.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}
	if !reflect.DeepEqual(newObj.Metadata, currentObj.Metadata) {
		return nil, api.StatusBadRequest("metadata is immutable")
	}
	if currentObj.ApiVersion != newObj.ApiVersion {
		return nil, api.StatusBadRequest("apiVersion is immutable")
	}
	if currentObj.Kind != newObj.Kind {
		return nil, api.StatusBadRequest("kind is immutable")
	}
	if !reflect.DeepEqual(currentObj.Spec, newObj.Spec) {
		return nil, api.StatusBadRequest("spec is immutable")
	}

	NilOutManagedObjectMetaProperties(&newObj.Metadata)
	newObj.Metadata.ResourceVersion = nil

	var updateCallback func(context.Context, uuid.UUID, *api.Device, *api.Device)

	if h.callbackManager != nil {
		updateCallback = h.callbackManager.DeviceUpdatedCallback
	}

	resourceEventFromUpdateDetailsFunc := func(ctx context.Context, update common.ResourceUpdate) *api.Event {
		return GetResourceEventFromUpdateDetails(ctx, api.DeviceKind, *currentObj.Metadata.Name, update.Reason, update.UpdateDetails, h.log)
	}
	updated := common.UpdateServiceSideStatus(ctx, orgId, newObj, currentObj, h.store, h.log, h.CreateEvent, resourceEventFromUpdateDetailsFunc)

	result, _, err := h.store.Device().Update(ctx, orgId, newObj, nil, true, DeviceVerificationCallback, updateCallback)
	status := StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
	if updated && err == nil {
		h.CreateEvent(ctx, GetResourceCreatedOrUpdatedEvent(ctx, false, api.DeviceKind, name, status, nil, h.log))
	}
	return result, status
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

	var updateCallback func(context.Context, uuid.UUID, *api.Device, *api.Device)

	if h.callbackManager != nil {
		updateCallback = h.callbackManager.DeviceUpdatedCallback
	}

	resourceEventFromUpdateDetailsFunc := func(ctx context.Context, update common.ResourceUpdate) *api.Event {
		return GetResourceEventFromUpdateDetails(ctx, api.DeviceKind, *currentObj.Metadata.Name, update.Reason, update.UpdateDetails, h.log)
	}
	updated := common.UpdateServiceSideStatus(ctx, orgId, newObj, currentObj, h.store, h.log, h.CreateEvent, resourceEventFromUpdateDetailsFunc)

	result, _, err := h.store.Device().Update(ctx, orgId, newObj, nil, true, DeviceVerificationCallback, updateCallback)
	status := StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
	if updated && err == nil {
		h.CreateEvent(ctx, GetResourceCreatedOrUpdatedEvent(ctx, false, api.DeviceKind, name, status, nil, h.log))
	}
	return result, status
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

	var updateCallback func(context.Context, uuid.UUID, *api.Device, *api.Device)

	if h.callbackManager != nil {
		updateCallback = h.callbackManager.DeviceUpdatedCallback
	}

	// set the fromAPI bool to 'false', otherwise updating the spec.decommissionRequested of a device is blocked
	result, updateDesc, err := h.store.Device().Update(ctx, orgId, deviceObj, []string{"status", "owner"}, false, DeviceVerificationCallback, updateCallback)
	status := StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
	if err != nil {
		h.CreateEvent(ctx, GetResourceDecommissionedEvent(ctx, api.DeviceKind, name, status, &updateDesc, h.log))
	}
	return result, status
}

func (h *ServiceHandler) UpdateDeviceAnnotations(ctx context.Context, name string, annotations map[string]string, deleteKeys []string) api.Status {
	orgId := store.NullOrgId
	err := h.store.Device().UpdateAnnotations(ctx, orgId, name, annotations, deleteKeys)
	return StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
}

func (h *ServiceHandler) UpdateRenderedDevice(ctx context.Context, name, renderedConfig, renderedApplications string) api.Status {
	orgId := store.NullOrgId
	err := h.store.Device().UpdateRendered(ctx, orgId, name, renderedConfig, renderedApplications)
	return StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
}

func (h *ServiceHandler) SetDeviceServiceConditions(ctx context.Context, name string, conditions []api.Condition) api.Status {
	orgId := store.NullOrgId
	err := h.store.Device().SetServiceConditions(ctx, orgId, name, conditions)
	return StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
}

func (h *ServiceHandler) OverwriteDeviceRepositoryRefs(ctx context.Context, name string, repositoryNames ...string) api.Status {
	orgId := store.NullOrgId
	err := h.store.Device().OverwriteRepositoryRefs(ctx, orgId, name, repositoryNames...)
	return StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
}

func (h *ServiceHandler) GetDeviceRepositoryRefs(ctx context.Context, name string) (*api.RepositoryList, api.Status) {
	orgId := store.NullOrgId
	result, err := h.store.Device().GetRepositoryRefs(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
}

func (h *ServiceHandler) CountDevices(ctx context.Context, params api.ListDevicesParams, annotationSelector *selector.AnnotationSelector) (int64, api.Status) {
	orgId := store.NullOrgId
	storeParams, status := convertDeviceListParams(params, annotationSelector)
	if status.Code != http.StatusOK {
		return 0, status
	}
	result, err := h.store.Device().Count(ctx, orgId, *storeParams)
	return result, StoreErrorToApiStatus(err, false, api.DeviceKind, nil)
}

func (h *ServiceHandler) UnmarkDevicesRolloutSelection(ctx context.Context, fleetName string) api.Status {
	orgId := store.NullOrgId
	err := h.store.Device().UnmarkRolloutSelection(ctx, orgId, fleetName)
	return StoreErrorToApiStatus(err, false, api.DeviceKind, nil)
}

func (h *ServiceHandler) MarkDevicesRolloutSelection(ctx context.Context, params api.ListDevicesParams, annotationSelector *selector.AnnotationSelector, limit *int) api.Status {
	orgId := store.NullOrgId
	storeParams, status := convertDeviceListParams(params, annotationSelector)
	if status.Code != http.StatusOK {
		return status
	}
	err := h.store.Device().MarkRolloutSelection(ctx, orgId, *storeParams, limit)
	return StoreErrorToApiStatus(err, false, api.DeviceKind, nil)
}

func (h *ServiceHandler) GetDeviceCompletionCounts(ctx context.Context, owner string, templateVersion string, updateTimeout *time.Duration) ([]api.DeviceCompletionCount, api.Status) {
	orgId := store.NullOrgId
	result, err := h.store.Device().CompletionCounts(ctx, orgId, owner, templateVersion, updateTimeout)
	return result, StoreErrorToApiStatus(err, false, api.DeviceKind, nil)
}

func (h *ServiceHandler) CountDevicesByLabels(ctx context.Context, params api.ListDevicesParams, annotationSelector *selector.AnnotationSelector, groupBy []string) ([]map[string]any, api.Status) {
	orgId := store.NullOrgId
	storeParams, status := convertDeviceListParams(params, annotationSelector)
	if status.Code != http.StatusOK {
		return nil, status
	}
	result, err := h.store.Device().CountByLabels(ctx, orgId, *storeParams, groupBy)
	return result, StoreErrorToApiStatus(err, false, api.DeviceKind, nil)
}

func (h *ServiceHandler) GetDevicesSummary(ctx context.Context, params api.ListDevicesParams, annotationSelector *selector.AnnotationSelector) (*api.DevicesSummary, api.Status) {
	orgId := store.NullOrgId
	storeParams, status := convertDeviceListParams(params, annotationSelector)
	if status.Code != http.StatusOK {
		return nil, status
	}
	result, err := h.store.Device().Summary(ctx, orgId, *storeParams)
	return result, StoreErrorToApiStatus(err, false, api.DeviceKind, nil)
}

func (h *ServiceHandler) UpdateDeviceSummaryStatusBatch(ctx context.Context, deviceNames []string, status api.DeviceSummaryStatusType, statusInfo string) api.Status {
	orgId := store.NullOrgId
	err := h.store.Device().UpdateSummaryStatusBatch(ctx, orgId, deviceNames, status, statusInfo)
	return StoreErrorToApiStatus(err, false, api.DeviceKind, nil)
}

func (h *ServiceHandler) UpdateServiceSideDeviceStatus(ctx context.Context, device api.Device) bool {
	orgId := store.NullOrgId
	return common.UpdateServiceSideStatus(ctx, orgId, &device, nil, h.store, h.log, nil, nil)
}
