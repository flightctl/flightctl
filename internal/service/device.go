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
	"github.com/flightctl/flightctl/internal/healthchecker"
	"github.com/flightctl/flightctl/internal/rendered"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/util/validation"
	"github.com/google/uuid"
	"github.com/samber/lo"
)

// PrepareDevicesAfterRestore performs post-restoration preparation tasks for devices
func (h *ServiceHandler) PrepareDevicesAfterRestore(ctx context.Context) error {
	h.log.Info("Starting post-restoration device preparation")

	// 1. Drop the KV store to clear all cached data
	h.log.Info("Clearing KV store after restoration")
	if h.kvStore != nil {
		if err := h.kvStore.DeleteAllKeys(ctx); err != nil {
			h.log.WithError(err).Error("Failed to clear KV store")
			return fmt.Errorf("failed to clear KV store: %w", err)
		}
		h.log.Info("KV store cleared successfully")
	} else {
		h.log.Warn("KV store not available, skipping clear")
	}

	// 2. Set waitForDeviceToReconnectAfterRestore annotation on all devices and unset lastSeen
	h.log.Info("Updating device annotations and clearing lastSeen timestamps")

	devicesUpdated, err := h.store.Device().PrepareDevicesAfterRestore(ctx)
	if err != nil {
		h.log.WithError(err).Error("Failed to prepare devices after restore")
		return fmt.Errorf("failed to prepare devices after restore: %w", err)
	}

	h.log.Infof("Post-restoration device preparation completed successfully. Updated %d devices total.", devicesUpdated)

	// Emit system restored event
	if h.eventHandler != nil {
		event := common.GetSystemRestoredEvent(ctx, devicesUpdated)
		if event != nil {
			h.eventHandler.CreateEvent(ctx, event)
			h.log.Info("System restored event created successfully")
		}
	}
	return nil
}

func (h *ServiceHandler) CreateDevice(ctx context.Context, device api.Device) (*api.Device, api.Status) {
	if device.Spec != nil && device.Spec.Decommissioning != nil {
		h.log.WithError(flterrors.ErrDecommission).Error("attempt to create decommissioned device")
		return nil, api.StatusBadRequest(flterrors.ErrDecommission.Error())
	}

	orgId := getOrgIdFromContext(ctx)

	// don't set fields that are managed by the service
	device.Status = nil
	NilOutManagedObjectMetaProperties(&device.Metadata)

	if errs := device.Validate(); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}

	_, _ = common.UpdateServiceSideStatus(ctx, orgId, &device, h.store, h.log)

	result, err := h.store.Device().Create(ctx, orgId, &device, h.callbackDeviceUpdated)
	return result, StoreErrorToApiStatus(err, true, api.DeviceKind, device.Metadata.Name)
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
	orgId := getOrgIdFromContext(ctx)
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

func (h *ServiceHandler) ListDevicesByServiceCondition(ctx context.Context, conditionType string, conditionStatus string, listParams store.ListParams) (*api.DeviceList, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	result, err := h.store.Device().ListDevicesByServiceCondition(ctx, orgId, conditionType, conditionStatus, listParams)
	return result, StoreErrorToApiStatus(err, false, api.DeviceKind, nil)
}

func (h *ServiceHandler) GetDevice(ctx context.Context, name string) (*api.Device, api.Status) {
	orgId := getOrgIdFromContext(ctx)

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

	orgId := getOrgIdFromContext(ctx)

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

	_, _ = common.UpdateServiceSideStatus(ctx, orgId, &device, h.store, h.log)

	result, created, err := h.store.Device().CreateOrUpdate(ctx, orgId, &device, fieldsToUnset, isNotInternal, DeviceVerificationCallback, h.callbackDeviceUpdated)
	return result, StoreErrorToApiStatus(err, created, api.DeviceKind, &name)
}

func (h *ServiceHandler) UpdateDevice(ctx context.Context, name string, device api.Device, fieldsToUnset []string) (*api.Device, error) {
	if device.Spec != nil && device.Spec.Decommissioning != nil {
		h.log.WithError(flterrors.ErrDecommission).Error("attempt to set decommissioned status when replacing device, or to replace decommissioned device")
		return nil, flterrors.ErrDecommission
	}

	orgId := getOrgIdFromContext(ctx)

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

	_, _ = common.UpdateServiceSideStatus(ctx, orgId, &device, h.store, h.log)

	return h.store.Device().Update(ctx, orgId, &device, fieldsToUnset, false, DeviceVerificationCallback, h.callbackDeviceUpdated)
}

func (h *ServiceHandler) DeleteDevice(ctx context.Context, name string) api.Status {
	orgId := getOrgIdFromContext(ctx)

	_, err := h.store.Device().Delete(ctx, orgId, name, h.callbackDeviceDeleted)
	return StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
}

// (GET /api/v1/devices/{name}/status)
func (h *ServiceHandler) GetDeviceStatus(ctx context.Context, name string) (*api.Device, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	result, err := h.store.Device().Get(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
}

func (h *ServiceHandler) GetDeviceLastSeen(ctx context.Context, name string) (*api.DeviceLastSeen, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	lastSeen, err := h.store.Device().GetLastSeen(ctx, orgId, name)
	if err != nil {
		return nil, StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
	}

	if lastSeen == nil {
		return nil, api.StatusNoContent()
	}

	return &api.DeviceLastSeen{
		LastSeen: *lastSeen,
	}, api.StatusOK()
}

func validateDeviceStatus(d *api.Device) []error {
	allErrs := append([]error{}, validation.ValidateResourceName(d.Metadata.Name)...)
	// TODO: implement validation of agent's status updates
	return allErrs
}

func (h *ServiceHandler) ReplaceDeviceStatus(ctx context.Context, name string, incomingDevice api.Device) (*api.Device, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	if errs := validateDeviceStatus(&incomingDevice); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}
	if incomingDevice.Metadata.Name == nil || *incomingDevice.Metadata.Name == "" {
		return nil, api.StatusBadRequest("device name is required")
	}
	if name != *incomingDevice.Metadata.Name {
		return nil, api.StatusBadRequest("resource name specified in metadata does not match name in path")
	}
	isNotInternal := !IsInternalRequest(ctx)
	if isNotInternal {
		incomingDevice.Status.LastSeen = lo.ToPtr(time.Now())
	}

	// UpdateServiceSideStatus() needs to know the latest .metadata.annotations[device-controller/renderedVersion]
	// that the agent does not provide or only have an outdated knowledge of
	originalDevice, err := h.store.Device().Get(ctx, orgId, name)
	if err != nil {
		return nil, StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
	}

	deviceToStore := &api.Device{}
	*deviceToStore = *originalDevice

	common.KeepDBDeviceStatus(&incomingDevice, deviceToStore)
	deviceToStore.Status = incomingDevice.Status

	_, annotationsChanged := common.UpdateServiceSideStatus(ctx, orgId, deviceToStore, h.store, h.log)

	// Use Update() if annotations changed, otherwise use UpdateStatus()
	var result *api.Device
	if annotationsChanged {
		h.log.Infof("Device %s: Annotations changed, using Update() to persist changes", *deviceToStore.Metadata.Name)
		result, err = h.store.Device().Update(ctx, orgId, deviceToStore, nil, false, DeviceVerificationCallback, h.callbackDeviceUpdated)
	} else {
		h.log.Debugf("Device %s: No annotation changes, using UpdateStatus()", *deviceToStore.Metadata.Name)
		result, err = h.store.Device().UpdateStatus(ctx, orgId, deviceToStore, h.callbackDeviceUpdated)
	}
	return result, StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
}

func (h *ServiceHandler) PatchDeviceStatus(ctx context.Context, name string, patch api.PatchRequest) (*api.Device, api.Status) {
	orgId := getOrgIdFromContext(ctx)

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

	_, _ = common.UpdateServiceSideStatus(ctx, orgId, newObj, h.store, h.log)

	result, err := h.store.Device().Update(ctx, orgId, newObj, nil, true, DeviceVerificationCallback, h.callbackDeviceUpdated)
	return result, StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
}

func (h *ServiceHandler) GetRenderedDevice(ctx context.Context, name string, params api.GetRenderedDeviceParams) (*api.Device, api.Status) {
	var (
		isNew             bool
		kvRenderedVersion string
		err               error
	)

	if _, ok := ctx.Value(consts.AgentCtxKey).(string); ok {
		if err := healthchecker.HealthChecks.Instance().Add(ctx, getOrgIdFromContext(ctx), name); err != nil {
			h.log.WithError(err).Errorf("failed to add healthcheck to device %s", name)
			return nil, api.StatusInternalServerError(fmt.Sprintf("failed to add healthcheck to device %s: %v", name, err))
		}
	}

	orgId := getOrgIdFromContext(ctx)

	if params.KnownRenderedVersion != nil {
		isNew, kvRenderedVersion, err = rendered.Bus.Instance().WaitForNewVersion(ctx, orgId, name, *params.KnownRenderedVersion)
		if err != nil {
			h.log.Errorf("GetRenderedDevice %s/%s: failed to wait for new rendered version: %v", orgId, name, err)
			return nil, api.StatusInternalServerError(fmt.Sprintf("failed to wait for new rendered version: %v", err))
		}
		if !isNew {
			return nil, api.StatusNoContent()
		}
	}

	result, err := h.store.Device().GetRendered(ctx, orgId, name, nil, h.agentEndpoint)
	if err != nil {
		h.log.Errorf("GetRenderedDevice %s/%s: failed to get rendered device: %v", orgId, name, err)
		return nil, StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
	}
	newVersion := result.Version()
	if kvRenderedVersion != "" && newVersion != "" && kvRenderedVersion != newVersion {
		// If the rendered version in the KV store is different from the one we just fetched,
		// we set the new version in the KV store.
		if err = rendered.Bus.Instance().StoreAndNotify(ctx, orgId, name, newVersion); err != nil {
			h.log.Errorf("GetRenderedDevice %s/%s: failed to set rendered version in kvstore: %v", orgId, name, err)
		}
	}
	return result, StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
}

// Only metadata.labels and spec can be patched. If we try to patch other fields, HTTP 400 Bad Request is returned.
func (h *ServiceHandler) PatchDevice(ctx context.Context, name string, patch api.PatchRequest) (*api.Device, api.Status) {
	orgId := getOrgIdFromContext(ctx)

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

	if errs := currentObj.ValidateUpdate(newObj); len(errs) > 0 {
		return nil, api.StatusBadRequest(errors.Join(errs...).Error())
	}
	if newObj.Spec != nil && newObj.Spec.Decommissioning != nil {
		return nil, api.StatusBadRequest("spec.decommissioning cannot be changed via patch request")
	}

	NilOutManagedObjectMetaProperties(&newObj.Metadata)
	newObj.Metadata.ResourceVersion = nil

	_, _ = common.UpdateServiceSideStatus(ctx, orgId, newObj, h.store, h.log)

	result, err := h.store.Device().Update(ctx, orgId, newObj, nil, true, DeviceVerificationCallback, h.callbackDeviceUpdated)
	return result, StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
}

func (h *ServiceHandler) SetOutOfDate(ctx context.Context, owner string) error {
	return h.store.Device().SetOutOfDate(ctx, getOrgIdFromContext(ctx), owner)
}

func (h *ServiceHandler) UpdateServerSideDeviceStatus(ctx context.Context, name string) error {
	orgId := getOrgIdFromContext(ctx)
	device, err := h.store.Device().GetWithoutServiceConditions(ctx, orgId, name)
	if err != nil {
		return err
	}
	if changed, _ := common.UpdateServiceSideStatus(ctx, orgId, device, h.store, h.log); changed {
		_, err = h.store.Device().UpdateStatus(ctx, orgId, device, h.callbackDeviceUpdated)
		if err != nil {
			h.log.WithError(err).Errorf("failed to update status for device %s/%s", orgId, name)
			return err
		}
	}
	return nil
}

func (h *ServiceHandler) DecommissionDevice(ctx context.Context, name string, decom api.DeviceDecommission) (*api.Device, api.Status) {
	orgId := getOrgIdFromContext(ctx)

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

	// set the fromAPI bool to 'false', otherwise updating the spec.decommissionRequested of a device is blocked
	result, err := h.store.Device().Update(ctx, orgId, deviceObj, []string{"status", "owner"}, false, DeviceVerificationCallback, h.callbackDeviceDecommission)
	return result, StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
}

func (h *ServiceHandler) UpdateDeviceAnnotations(ctx context.Context, name string, annotations map[string]string, deleteKeys []string) api.Status {
	orgId := getOrgIdFromContext(ctx)
	err := h.store.Device().UpdateAnnotations(ctx, orgId, name, annotations, deleteKeys)
	return StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
}

func (h *ServiceHandler) UpdateRenderedDevice(ctx context.Context, name, renderedConfig, renderedApplications, specHash string) api.Status {
	orgId := getOrgIdFromContext(ctx)
	renderedVersion, err := h.store.Device().UpdateRendered(ctx, orgId, name, renderedConfig, renderedApplications, specHash)
	if err != nil {
		h.log.Errorf("Failed to update rendered device %s/%s: %v", orgId, name, err)
		return StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
	}
	if renderedVersion == "" {
		h.log.Debugf("Rendered device %s/%s: no change in rendered version", orgId, name)
		return api.StatusOK()
	}
	err = h.UpdateServerSideDeviceStatus(ctx, name)
	if err != nil {
		return StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
	}

	err = rendered.Bus.Instance().StoreAndNotify(ctx, orgId, name, renderedVersion)
	if err != nil {
		h.log.Errorf("Failed to publish rendered device %s/%s: %v", orgId, name, err)
		return api.StatusInternalServerError(fmt.Sprintf("failed to publish rendered device: %v", err))
	}
	return StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
}

func (h *ServiceHandler) SetDeviceServiceConditions(ctx context.Context, name string, conditions []api.Condition) api.Status {
	orgId := getOrgIdFromContext(ctx)

	// Create callback to handle condition changes
	callback := func(ctx context.Context, orgId uuid.UUID, device *api.Device, oldConditions, newConditions []api.Condition) {
		h.diffAndEmitConditionEvents(ctx, device, oldConditions, newConditions)
	}

	err := h.store.Device().SetServiceConditions(ctx, orgId, name, conditions, callback)
	return StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
}

// diffAndEmitConditionEvents compares old and new conditions and emits events for condition changes
func (h *ServiceHandler) diffAndEmitConditionEvents(ctx context.Context, device *api.Device, oldConditions, newConditions []api.Condition) {
	// Track condition changes for MultipleOwners
	oldMultipleOwnersCondition := api.FindStatusCondition(oldConditions, api.ConditionTypeDeviceMultipleOwners)
	newMultipleOwnersCondition := api.FindStatusCondition(newConditions, api.ConditionTypeDeviceMultipleOwners)

	// Check if MultipleOwners condition changed
	multipleOwnersConditionChanged := hasConditionChanged(oldMultipleOwnersCondition, newMultipleOwnersCondition)

	if multipleOwnersConditionChanged {
		common.EmitMultipleOwnersEvents(ctx, device, oldMultipleOwnersCondition, newMultipleOwnersCondition,
			h.CreateEvent, common.GetDeviceMultipleOwnersDetectedEvent, common.GetDeviceMultipleOwnersResolvedEvent,
			h.log,
		)
	}

	// Track condition changes for SpecValid
	oldSpecValidCondition := api.FindStatusCondition(oldConditions, api.ConditionTypeDeviceSpecValid)
	newSpecValidCondition := api.FindStatusCondition(newConditions, api.ConditionTypeDeviceSpecValid)

	// Check if SpecValid condition changed
	specValidConditionChanged := hasConditionChanged(oldSpecValidCondition, newSpecValidCondition)

	if specValidConditionChanged {
		common.EmitSpecValidEvents(ctx, device, oldSpecValidCondition, newSpecValidCondition,
			h.CreateEvent, common.GetDeviceSpecValidEvent, common.GetDeviceSpecInvalidEvent,
			h.log)
	}
}

// hasConditionChanged checks if a condition actually changed between old and new
func hasConditionChanged(oldCondition, newCondition *api.Condition) bool {
	if oldCondition == nil && newCondition == nil {
		return false
	}
	if oldCondition == nil || newCondition == nil {
		return true
	}

	changed := oldCondition.Status != newCondition.Status ||
		oldCondition.Reason != newCondition.Reason ||
		oldCondition.Message != newCondition.Message

	return changed
}

func (h *ServiceHandler) OverwriteDeviceRepositoryRefs(ctx context.Context, name string, repositoryNames ...string) api.Status {
	orgId := getOrgIdFromContext(ctx)
	err := h.store.Device().OverwriteRepositoryRefs(ctx, orgId, name, repositoryNames...)
	return StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
}

func (h *ServiceHandler) GetDeviceRepositoryRefs(ctx context.Context, name string) (*api.RepositoryList, api.Status) {
	orgId := getOrgIdFromContext(ctx)
	result, err := h.store.Device().GetRepositoryRefs(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, api.DeviceKind, &name)
}

func (h *ServiceHandler) CountDevices(ctx context.Context, params api.ListDevicesParams, annotationSelector *selector.AnnotationSelector) (int64, api.Status) {
	orgId := getOrgIdFromContext(ctx)
	storeParams, status := convertDeviceListParams(params, annotationSelector)
	if status.Code != http.StatusOK {
		return 0, status
	}
	result, err := h.store.Device().Count(ctx, orgId, *storeParams)
	return result, StoreErrorToApiStatus(err, false, api.DeviceKind, nil)
}

func (h *ServiceHandler) UnmarkDevicesRolloutSelection(ctx context.Context, fleetName string) api.Status {
	orgId := getOrgIdFromContext(ctx)
	err := h.store.Device().UnmarkRolloutSelection(ctx, orgId, fleetName)
	return StoreErrorToApiStatus(err, false, api.DeviceKind, nil)
}

func (h *ServiceHandler) MarkDevicesRolloutSelection(ctx context.Context, params api.ListDevicesParams, annotationSelector *selector.AnnotationSelector, limit *int) api.Status {
	orgId := getOrgIdFromContext(ctx)
	storeParams, status := convertDeviceListParams(params, annotationSelector)
	if status.Code != http.StatusOK {
		return status
	}
	err := h.store.Device().MarkRolloutSelection(ctx, orgId, *storeParams, limit)
	return StoreErrorToApiStatus(err, false, api.DeviceKind, nil)
}

func (h *ServiceHandler) GetDeviceCompletionCounts(ctx context.Context, owner string, templateVersion string, updateTimeout *time.Duration) ([]api.DeviceCompletionCount, api.Status) {
	orgId := getOrgIdFromContext(ctx)
	result, err := h.store.Device().CompletionCounts(ctx, orgId, owner, templateVersion, updateTimeout)
	return result, StoreErrorToApiStatus(err, false, api.DeviceKind, nil)
}

func (h *ServiceHandler) CountDevicesByLabels(ctx context.Context, params api.ListDevicesParams, annotationSelector *selector.AnnotationSelector, groupBy []string) ([]map[string]any, api.Status) {
	orgId := getOrgIdFromContext(ctx)
	storeParams, status := convertDeviceListParams(params, annotationSelector)
	if status.Code != http.StatusOK {
		return nil, status
	}
	result, err := h.store.Device().CountByLabels(ctx, orgId, *storeParams, groupBy)
	return result, StoreErrorToApiStatus(err, false, api.DeviceKind, nil)
}

func (h *ServiceHandler) GetDevicesSummary(ctx context.Context, params api.ListDevicesParams, annotationSelector *selector.AnnotationSelector) (*api.DevicesSummary, api.Status) {
	orgId := getOrgIdFromContext(ctx)
	storeParams, status := convertDeviceListParams(params, annotationSelector)
	if status.Code != http.StatusOK {
		return nil, status
	}
	result, err := h.store.Device().Summary(ctx, orgId, *storeParams)
	return result, StoreErrorToApiStatus(err, false, api.DeviceKind, nil)
}

func (h *ServiceHandler) UpdateServiceSideDeviceStatus(ctx context.Context, device api.Device) bool {
	orgId := getOrgIdFromContext(ctx)
	anyChanged, _ := common.UpdateServiceSideStatus(ctx, orgId, &device, h.store, h.log)
	return anyChanged
}

func (h *ServiceHandler) ResumeDevices(ctx context.Context, request api.DeviceResumeRequest) (api.DeviceResumeResponse, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	h.log.Infof("ResumeDevices called with label selector: %v, field selector: %v",
		request.LabelSelector, request.FieldSelector)

	// Create list params with both label and field selectors
	listParams, status := prepareListParams(nil, request.LabelSelector, request.FieldSelector, nil)
	if status.Code != http.StatusOK {
		return api.DeviceResumeResponse{}, status
	}

	// Remove conflictPaused annotation from all matching devices in a single SQL query
	resumedCount, deviceIDs, err := h.store.Device().RemoveConflictPausedAnnotation(ctx, orgId, lo.FromPtr(listParams))
	if err != nil {
		var se *selector.SelectorError
		switch {
		case selector.AsSelectorError(err, &se):
			return api.DeviceResumeResponse{}, api.StatusBadRequest(se.Error())
		default:
			return api.DeviceResumeResponse{}, api.StatusInternalServerError(fmt.Sprintf("failed to resume devices: %v", err))
		}
	}

	h.log.Infof("Resumed %d devices: %v", resumedCount, deviceIDs)

	// Emit DeviceConflictResolved events for each resumed device
	if h.eventHandler != nil {
		for _, deviceID := range deviceIDs {
			event := common.GetDeviceConflictResolvedEvent(ctx, deviceID)
			h.eventHandler.CreateEvent(ctx, event)
		}
		h.log.Infof("Created DeviceConflictResolved events for %d devices", len(deviceIDs))
	}

	return api.DeviceResumeResponse{
		ResumedDevices: int(resumedCount),
	}, api.StatusOK()
}

// callbackDeviceUpdated is the device-specific callback that handles device events
func (h *ServiceHandler) callbackDeviceUpdated(ctx context.Context, resourceKind api.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	h.eventHandler.HandleDeviceUpdatedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
}

// callbackDeviceDecommission is the device-specific callback that handles device decommission events
func (h *ServiceHandler) callbackDeviceDecommission(ctx context.Context, resourceKind api.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	h.eventHandler.HandleDeviceDecommissionEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
}

// callbackDeviceDeleted is the device-specific callback that handles device deletion events
func (h *ServiceHandler) callbackDeviceDeleted(ctx context.Context, resourceKind api.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	h.eventHandler.HandleGenericResourceDeletedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
}
