package device

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"time"

	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/healthchecker"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/rendered"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/store"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/util/validation"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/semaphore"
)

// DeviceServiceHandler implements Service.
type DeviceServiceHandler struct {
	deviceStore   devicestore.Store
	fleetStore    fleetstore.Store
	events        events.Service
	kvStore       kvstore.KVStore
	agentGate     *semaphore.Weighted
	agentEndpoint string
	log           logrus.FieldLogger
}

// NewDeviceServiceHandler creates a new DeviceServiceHandler instance.
func NewDeviceServiceHandler(
	deviceStore devicestore.Store,
	fleetStore fleetstore.Store,
	events events.Service,
	kvStore kvstore.KVStore,
	agentEndpoint string,
	log logrus.FieldLogger,
) Service {
	return &DeviceServiceHandler{
		deviceStore:   deviceStore,
		fleetStore:    fleetStore,
		events:        events,
		kvStore:       kvStore,
		agentGate:     semaphore.NewWeighted(common.MaxConcurrentAgents),
		agentEndpoint: agentEndpoint,
		log:           log,
	}
}

var _ Service = (*DeviceServiceHandler)(nil)

// SanitizeDevice clears status and managed metadata from an untrusted device document
// (HTTP body). Trusted callers that must preserve Owner/annotations must not use this.
func SanitizeDevice(device *domain.Device) {
	if device == nil {
		return
	}
	device.Status = nil
	common.NilOutManagedObjectMetaProperties(&device.Metadata)
}

// CreateDeviceFromUntrusted sanitizes an untrusted device document, then creates it.
func CreateDeviceFromUntrusted(ctx context.Context, svc Service, orgId uuid.UUID, device domain.Device) (*domain.Device, domain.Status) {
	SanitizeDevice(&device)
	return svc.CreateDevice(ctx, orgId, device)
}

// ReplaceDeviceFromUntrusted sanitizes an untrusted device document, then replaces it.
func ReplaceDeviceFromUntrusted(ctx context.Context, svc Service, orgId uuid.UUID, name string, device domain.Device, fieldsToUnset []string, enforceOwnership bool) (*domain.Device, domain.Status) {
	SanitizeDevice(&device)
	return svc.ReplaceDevice(ctx, orgId, name, device, fieldsToUnset, enforceOwnership)
}

func (h *DeviceServiceHandler) CreateDevice(ctx context.Context, orgId uuid.UUID, device domain.Device) (*domain.Device, domain.Status) {
	if device.Spec != nil && device.Spec.Decommissioning != nil {
		h.log.WithError(flterrors.ErrDecommission).Error("attempt to create decommissioned device")
		return nil, domain.StatusBadRequest(flterrors.ErrDecommission.Error())
	}

	if errs := device.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}

	_ = common.UpdateServiceSideStatus(ctx, orgId, &device, h.fleetStore, h.log)

	result, err := h.deviceStore.Create(ctx, orgId, &device, h.callbackDeviceUpdated)
	return result, common.StoreErrorToApiStatus(err, true, domain.DeviceKind, device.Metadata.Name)
}

func convertDeviceListParams(params domain.ListDevicesParams, annotationSelector *selector.AnnotationSelector) (*devicestore.DeviceListParams, domain.Status) {
	listParams, status := common.PrepareListParams(params.Continue, params.LabelSelector, params.FieldSelector, params.Limit)
	if status != domain.StatusOK() {
		return nil, status
	}
	listParams.AnnotationSelector = annotationSelector
	return &devicestore.DeviceListParams{
		ListParams: *listParams,
		CveID:      params.CveId,
	}, domain.StatusOK()
}

func (h *DeviceServiceHandler) ListDevices(ctx context.Context, orgId uuid.UUID, params domain.ListDevicesParams, annotationSelector *selector.AnnotationSelector) (*domain.DeviceList, domain.Status) {
	storeParams, status := convertDeviceListParams(params, annotationSelector)
	if status.Code != http.StatusOK {
		return nil, status
	}

	// Check if SummaryOnly is true
	if params.SummaryOnly != nil && *params.SummaryOnly {
		// Check for unsupported parameters
		if params.Limit != nil || params.Continue != nil {
			return nil, domain.StatusBadRequest("parameters such as 'limit', and 'continue' are not supported when 'summaryOnly' is true")
		}

		result, err := h.deviceStore.Summary(ctx, orgId, storeParams.ListParams)

		switch err {
		case nil:
			// Create an empty DeviceList and set the summary
			emptyList, _ := model.DevicesToApiResource([]model.Device{}, nil, nil)
			emptyList.Summary = result
			return &emptyList, domain.StatusOK()
		default:
			return nil, domain.StatusInternalServerError(err.Error())
		}
	}

	if storeParams.Limit == 0 {
		storeParams.Limit = common.MaxRecordsPerListRequest
	} else if storeParams.Limit > common.MaxRecordsPerListRequest {
		return nil, domain.StatusBadRequest(fmt.Sprintf("limit cannot exceed %d", common.MaxRecordsPerListRequest))
	} else if storeParams.Limit < 0 {
		return nil, domain.StatusBadRequest("limit cannot be negative")
	}

	result, err := h.deviceStore.List(ctx, orgId, *storeParams)
	if err == nil {
		return result, domain.StatusOK()
	}

	var se *selector.SelectorError

	switch {
	case selector.AsSelectorError(err, &se):
		return nil, domain.StatusBadRequest(se.Error())
	default:
		return nil, domain.StatusInternalServerError(err.Error())
	}
}

func (h *DeviceServiceHandler) ListConnectivityChangedDevices(ctx context.Context, orgId uuid.UUID, params domain.ListDevicesParams, cutoffTime time.Time) (*domain.DeviceList, domain.Status) {
	storeParams, status := convertDeviceListParams(params, nil)
	if status.Code != http.StatusOK {
		return nil, status
	}

	// Check if SummaryOnly is true
	if params.SummaryOnly != nil && *params.SummaryOnly {
		// Check for unsupported parameters
		return nil, domain.StatusBadRequest("summaryOnly is not supported for disconnected devices")
	}

	if params.FieldSelector != nil {
		return nil, domain.StatusBadRequest("fieldSelector is not supported for disconnected devices")
	}

	if params.LabelSelector != nil {
		return nil, domain.StatusBadRequest("labelSelector is not supported for disconnected devices")
	}

	if storeParams.Limit == 0 {
		storeParams.Limit = common.MaxRecordsPerListRequest
	} else if storeParams.Limit > common.MaxRecordsPerListRequest {
		return nil, domain.StatusBadRequest(fmt.Sprintf("limit cannot exceed %d", common.MaxRecordsPerListRequest))
	} else if storeParams.Limit < 0 {
		return nil, domain.StatusBadRequest("limit cannot be negative")
	}

	result, err := h.deviceStore.ListConnectivityChanged(ctx, orgId, storeParams.ListParams, cutoffTime)
	if err == nil {
		return result, domain.StatusOK()
	}

	var se *selector.SelectorError

	switch {
	case selector.AsSelectorError(err, &se):
		return nil, domain.StatusBadRequest(se.Error())
	default:
		return nil, domain.StatusInternalServerError(err.Error())
	}

}

func (h *DeviceServiceHandler) ListDevicesByServiceCondition(ctx context.Context, orgId uuid.UUID, conditionType string, conditionStatus string, listParams store.ListParams) (*domain.DeviceList, domain.Status) {
	result, err := h.deviceStore.ListDevicesByServiceCondition(ctx, orgId, conditionType, conditionStatus, listParams)
	return result, common.StoreErrorToApiStatus(err, false, domain.DeviceKind, nil)
}

func (h *DeviceServiceHandler) GetDevice(ctx context.Context, orgId uuid.UUID, name string) (*domain.Device, domain.Status) {
	result, err := h.deviceStore.Get(ctx, orgId, name)
	return result, common.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
}

// DeviceVerificationCallback ensures the device wasn't decommissioned before an update proceeds.
func DeviceVerificationCallback(ctx context.Context, before, after *domain.Device) error {
	if before != nil && before.Spec != nil && before.Spec.Decommissioning != nil {
		return flterrors.ErrDecommission
	}
	return nil
}

func (h *DeviceServiceHandler) ReplaceDevice(ctx context.Context, orgId uuid.UUID, name string, device domain.Device, fieldsToUnset []string, enforceOwnership bool) (*domain.Device, domain.Status) {
	if device.Spec != nil && device.Spec.Decommissioning != nil {
		h.log.WithError(flterrors.ErrDecommission).Error("attempt to set decommissioned status when replacing device, or to replace decommissioned device")
		return nil, domain.StatusBadRequest(flterrors.ErrDecommission.Error())
	}

	if errs := device.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}
	if name != *device.Metadata.Name {
		return nil, domain.StatusBadRequest("resource name specified in metadata does not match name in path")
	}

	if enforceOwnership {
		existing, getErr := h.deviceStore.Get(ctx, orgId, name)
		if getErr != nil {
			if !errors.Is(getErr, flterrors.ErrResourceNotFound) {
				return nil, common.StoreErrorToApiStatus(getErr, false, domain.DeviceKind, &name)
			}
		} else if len(lo.FromPtr(existing.Metadata.Owner)) != 0 {
			if !domain.DeviceSpecsAreEqual(lo.FromPtr(existing.Spec), lo.FromPtr(device.Spec)) {
				return nil, common.StoreErrorToApiStatus(flterrors.ErrUpdatingResourceWithOwnerNotAllowed, false, domain.DeviceKind, &name)
			}
		}
	}

	_ = common.UpdateServiceSideStatus(ctx, orgId, &device, h.fleetStore, h.log)

	result, created, err := h.deviceStore.CreateOrUpdate(ctx, orgId, &device, fieldsToUnset, DeviceVerificationCallback, h.callbackDeviceUpdated)
	return result, common.StoreErrorToApiStatus(err, created, domain.DeviceKind, &name)
}

func (h *DeviceServiceHandler) UpdateDevice(ctx context.Context, orgId uuid.UUID, name string, device domain.Device, fieldsToUnset []string) (*domain.Device, error) {
	if device.Spec != nil && device.Spec.Decommissioning != nil {
		h.log.WithError(flterrors.ErrDecommission).Error("attempt to set decommissioned status when replacing device, or to replace decommissioned device")
		return nil, flterrors.ErrDecommission
	}

	if errs := device.Validate(); len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if name != *device.Metadata.Name {
		return nil, fmt.Errorf("resource name specified in metadata does not match name in path")
	}

	_ = common.UpdateServiceSideStatus(ctx, orgId, &device, h.fleetStore, h.log)

	// Ownership is never enforced on UpdateDevice (agent/console trusted path).
	return h.deviceStore.Update(ctx, orgId, &device, fieldsToUnset, DeviceVerificationCallback, h.callbackDeviceUpdated)
}

func (h *DeviceServiceHandler) DeleteDevice(ctx context.Context, orgId uuid.UUID, name string) domain.Status {
	_, err := h.deviceStore.Delete(ctx, orgId, name, h.callbackDeviceDeleted)
	return common.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
}

// (GET /api/v1/devices/{name}/status)
func (h *DeviceServiceHandler) GetDeviceStatus(ctx context.Context, orgId uuid.UUID, name string) (*domain.Device, domain.Status) {
	result, err := h.deviceStore.Get(ctx, orgId, name)
	return result, common.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
}

func (h *DeviceServiceHandler) GetDeviceLastSeen(ctx context.Context, orgId uuid.UUID, name string) (*domain.DeviceLastSeen, domain.Status) {
	lastSeen, err := h.deviceStore.GetLastSeen(ctx, orgId, name)
	if err != nil {
		return nil, common.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
	}

	if lastSeen == nil {
		return nil, domain.StatusNoContent()
	}

	return &domain.DeviceLastSeen{
		LastSeen: *lastSeen,
	}, domain.StatusOK()
}

func validateDeviceStatus(d *domain.Device) []error {
	allErrs := append([]error{}, validation.ValidateResourceName(d.Metadata.Name)...)
	// TODO: implement validation of agent's status updates
	return allErrs
}

func (h *DeviceServiceHandler) ReplaceDeviceStatus(ctx context.Context, orgId uuid.UUID, name string, incomingDevice domain.Device, refreshLastSeen bool) (*domain.Device, domain.Status) {
	if errs := validateDeviceStatus(&incomingDevice); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}
	if incomingDevice.Metadata.Name == nil || *incomingDevice.Metadata.Name == "" {
		return nil, domain.StatusBadRequest("device name is required")
	}
	if name != *incomingDevice.Metadata.Name {
		return nil, domain.StatusBadRequest("resource name specified in metadata does not match name in path")
	}
	if incomingDevice.Status == nil {
		return nil, domain.StatusBadRequest("device status is required")
	}
	if refreshLastSeen {
		if h.agentGate.Acquire(ctx, 1) == nil {
			defer h.agentGate.Release(1)
		}
		incomingDevice.Status.LastSeen = lo.ToPtr(time.Now())
	}

	// UpdateServiceSideStatus() needs to know the latest .metadata.annotations[device-controller/renderedVersion]
	// that the agent does not provide or only have an outdated knowledge of
	originalDevice, err := h.deviceStore.GetWithTimestamp(ctx, orgId, name)
	if err != nil {
		return nil, common.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
	}

	deviceToStore := &domain.Device{}
	*deviceToStore = *originalDevice

	common.KeepDBDeviceStatus(&incomingDevice, deviceToStore)
	deviceToStore.Status = incomingDevice.Status
	_ = common.UpdateServiceSideStatus(ctx, orgId, deviceToStore, h.fleetStore, h.log)

	result, err := h.deviceStore.UpdateStatus(ctx, orgId, deviceToStore, h.callbackDeviceUpdated)
	return result, common.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
}

func (h *DeviceServiceHandler) PatchDeviceStatus(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest) (*domain.Device, domain.Status) {
	currentObj, err := h.deviceStore.Get(ctx, orgId, name)
	if err != nil {
		return nil, common.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
	}

	newObj := &domain.Device{}
	err = common.ApplyJSONPatch(ctx, currentObj, newObj, patch, "/devices/"+name)
	if err != nil {
		return nil, domain.StatusBadRequest(err.Error())
	}

	if errs := validateDeviceStatus(newObj); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}

	if errs := newObj.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}
	if !reflect.DeepEqual(newObj.Metadata, currentObj.Metadata) {
		return nil, domain.StatusBadRequest("metadata is immutable")
	}
	if currentObj.ApiVersion != newObj.ApiVersion {
		return nil, domain.StatusBadRequest("apiVersion is immutable")
	}
	if currentObj.Kind != newObj.Kind {
		return nil, domain.StatusBadRequest("kind is immutable")
	}
	if !reflect.DeepEqual(currentObj.Spec, newObj.Spec) {
		return nil, domain.StatusBadRequest("spec is immutable")
	}

	common.NilOutManagedObjectMetaProperties(&newObj.Metadata)
	newObj.Metadata.ResourceVersion = nil

	_ = common.UpdateServiceSideStatus(ctx, orgId, newObj, h.fleetStore, h.log)

	result, err := h.deviceStore.Update(ctx, orgId, newObj, nil, DeviceVerificationCallback, h.callbackDeviceUpdated)
	return result, common.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
}

func (h *DeviceServiceHandler) GetRenderedDevice(ctx context.Context, orgId uuid.UUID, name string, params domain.GetRenderedDeviceParams) (*domain.Device, domain.Status) {
	var (
		kvRenderedVersion       string
		err                     error
		isAgent                 bool
		processedAwaitReconnect bool
	)

	if _, isAgent = ctx.Value(consts.AgentCtxKey).(string); isAgent {
		if err := healthchecker.HealthChecks.Instance().Add(ctx, orgId, name); err != nil {
			h.log.WithError(err).Errorf("failed to add healthcheck to device %s", name)
			return nil, domain.StatusInternalServerError(fmt.Sprintf("failed to add healthcheck to device %s: %v", name, err))
		}

		// Process awaiting reconnect annotation if present and KV store contains the awaiting reconnection key
		processedAwaitReconnect = h.processAwaitingReconnectIfNeeded(ctx, orgId, name, params.KnownRenderedVersion)
	}

	if params.KnownRenderedVersion != nil && !processedAwaitReconnect {
		n, gotNotification, err := rendered.Bus.Instance().WaitForNotification(ctx, orgId, name, *params.KnownRenderedVersion)
		if err != nil {
			h.log.Errorf("GetRenderedDevice %s/%s: failed to wait for notification: %v", orgId, name, err)
			return nil, domain.StatusInternalServerError(fmt.Sprintf("failed to wait for notification: %v", err))
		}
		if !gotNotification {
			return nil, domain.StatusNoContent()
		}
		switch n.Type {
		case rendered.NotificationTypeSpecUpdated:
			kvRenderedVersion = n.RenderedVersion
		case rendered.NotificationTypeConsole:
			if err := rendered.Bus.Instance().ClearConsoleNotification(ctx, orgId, name); err != nil {
				h.log.Warnf("GetRenderedDevice %s/%s: failed to clear console notification: %v", orgId, name, err)
			}
		}
	}
	// When processedAwaitReconnect we skip WaitForNotification and return the current device (200)
	// so the agent sees the updated state and re-pushes its status.

	if isAgent {
		if h.agentGate.Acquire(ctx, 1) == nil {
			defer h.agentGate.Release(1)
		}
	}

	result, err := h.deviceStore.GetRendered(ctx, orgId, name, nil, h.agentEndpoint)
	if err != nil {
		h.log.Errorf("GetRenderedDevice %s/%s: failed to get rendered device: %v", orgId, name, err)
		return nil, common.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
	}
	newVersion := result.Version()
	if kvRenderedVersion != "" && newVersion != "" && kvRenderedVersion != newVersion {
		// If the rendered version in the KV store is different from the one we just fetched,
		// we set the new version in the KV store.
		if err = rendered.Bus.Instance().StoreAndNotify(ctx, orgId, name, newVersion); err != nil {
			h.log.Errorf("GetRenderedDevice %s/%s: failed to set rendered version in kvstore: %v", orgId, name, err)
		}
	}
	return result, common.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
}

// Only metadata.labels and spec can be patched. If we try to patch other fields, HTTP 400 Bad Request is returned.
func (h *DeviceServiceHandler) PatchDevice(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest, enforceOwnership bool) (*domain.Device, domain.Status) {
	currentObj, err := h.deviceStore.Get(ctx, orgId, name)
	if err != nil {
		return nil, common.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
	}

	newObj := &domain.Device{}
	err = common.ApplyJSONPatch(ctx, currentObj, newObj, patch, "/devices/"+name)
	if err != nil {
		return nil, domain.StatusBadRequest(err.Error())
	}

	// Status.LastSeen and Status.SystemInfo.AdditionalProperties are not marshaled into newObj by ApplyJSONPatch
	// and will always be set to nil as they have "-" json tags and will not be copied into newObj.  For now, set the fields manually
	// so later validation passes
	if currentObj.Status != nil {
		newObj.Status.LastSeen = currentObj.Status.LastSeen
		newObj.Status.SystemInfo.AdditionalProperties = currentObj.Status.SystemInfo.AdditionalProperties
	}

	if errs := newObj.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}

	if errs := currentObj.ValidateUpdate(newObj); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}
	if newObj.Spec != nil && newObj.Spec.Decommissioning != nil {
		return nil, domain.StatusBadRequest("spec.decommissioning cannot be changed via patch request")
	}

	common.NilOutManagedObjectMetaProperties(&newObj.Metadata)
	newObj.Metadata.ResourceVersion = nil

	if enforceOwnership && len(lo.FromPtr(currentObj.Metadata.Owner)) != 0 {
		if !domain.DeviceSpecsAreEqual(lo.FromPtr(currentObj.Spec), lo.FromPtr(newObj.Spec)) {
			return nil, common.StoreErrorToApiStatus(flterrors.ErrUpdatingResourceWithOwnerNotAllowed, false, domain.DeviceKind, &name)
		}
	}

	_ = common.UpdateServiceSideStatus(ctx, orgId, newObj, h.fleetStore, h.log)

	result, err := h.deviceStore.Update(ctx, orgId, newObj, nil, DeviceVerificationCallback, h.callbackDeviceUpdated)
	return result, common.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
}

func (h *DeviceServiceHandler) SetOutOfDate(ctx context.Context, orgId uuid.UUID, owner string) error {
	return h.deviceStore.SetOutOfDate(ctx, orgId, owner)
}

func (h *DeviceServiceHandler) UpdateServerSideDeviceStatus(ctx context.Context, orgId uuid.UUID, name string) error {
	device, err := h.deviceStore.GetWithTimestamp(ctx, orgId, name)
	if err != nil {
		return err
	}
	if changed := common.UpdateServiceSideStatus(ctx, orgId, device, h.fleetStore, h.log); changed {
		_, err = h.deviceStore.UpdateStatus(ctx, orgId, device, h.callbackDeviceUpdated)
		if err != nil {
			h.log.WithError(err).Errorf("failed to update status for device %s/%s", orgId, name)
			return err
		}
	}
	return nil
}

func (h *DeviceServiceHandler) DecommissionDevice(ctx context.Context, orgId uuid.UUID, name string, decom domain.DeviceDecommission) (*domain.Device, domain.Status) {
	result, err := h.deviceStore.DecommissionDevice(ctx, orgId, name, decom, h.callbackDeviceDecommission)
	return result, common.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
}

func (h *DeviceServiceHandler) UpdateDeviceAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) domain.Status {
	err := h.deviceStore.UpdateAnnotations(ctx, orgId, name, annotations, deleteKeys)
	return common.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
}

func (h *DeviceServiceHandler) UpdateRenderedDevice(ctx context.Context, orgId uuid.UUID, name, renderedConfig, renderedApplications, specHash string, configFingerprints []domain.DependencySyncConfigRefStatus, forceUpdate bool) domain.Status {
	renderedVersion, err := h.deviceStore.UpdateRendered(ctx, orgId, name, renderedConfig, renderedApplications, specHash, configFingerprints, forceUpdate)
	if err != nil {
		h.log.Errorf("Failed to update rendered device %s/%s: %v", orgId, name, err)
		return common.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
	}
	if renderedVersion == "" {
		h.log.Debugf("Rendered device %s/%s: no change in rendered version", orgId, name)
		return domain.StatusOK()
	}
	err = h.UpdateServerSideDeviceStatus(ctx, orgId, name)
	if err != nil {
		return common.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
	}

	err = rendered.Bus.Instance().StoreAndNotify(ctx, orgId, name, renderedVersion)
	if err != nil {
		h.log.Errorf("Failed to publish rendered device %s/%s: %v", orgId, name, err)
		return domain.StatusInternalServerError(fmt.Sprintf("failed to publish rendered device: %v", err))
	}
	return common.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
}

func (h *DeviceServiceHandler) SetDeviceServiceConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []domain.Condition) domain.Status {
	callback := func(ctx context.Context, orgId uuid.UUID, device *domain.Device, oldConditions, newConditions []domain.Condition) {
		h.diffAndEmitConditionEvents(ctx, orgId, device, oldConditions, newConditions)
	}

	err := h.deviceStore.SetServiceConditions(ctx, orgId, name, conditions, callback)
	return common.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
}

// diffAndEmitConditionEvents compares old and new conditions and emits events for condition changes
func (h *DeviceServiceHandler) diffAndEmitConditionEvents(ctx context.Context, orgId uuid.UUID, device *domain.Device, oldConditions, newConditions []domain.Condition) {
	// Track condition changes for MultipleOwners
	oldMultipleOwnersCondition := domain.FindStatusCondition(oldConditions, domain.ConditionTypeDeviceMultipleOwners)
	newMultipleOwnersCondition := domain.FindStatusCondition(newConditions, domain.ConditionTypeDeviceMultipleOwners)

	// Check if MultipleOwners condition changed
	multipleOwnersConditionChanged := common.HasConditionChanged(oldMultipleOwnersCondition, newMultipleOwnersCondition)

	if multipleOwnersConditionChanged {
		createEvent := func(c context.Context, e *domain.Event) { h.events.CreateEvent(c, orgId, e) }
		common.EmitMultipleOwnersEvents(ctx, device, oldMultipleOwnersCondition, newMultipleOwnersCondition,
			createEvent, common.GetDeviceMultipleOwnersDetectedEvent, common.GetDeviceMultipleOwnersResolvedEvent,
			h.log,
		)
	}

	// Track condition changes for SpecValid
	oldSpecValidCondition := domain.FindStatusCondition(oldConditions, domain.ConditionTypeDeviceSpecValid)
	newSpecValidCondition := domain.FindStatusCondition(newConditions, domain.ConditionTypeDeviceSpecValid)

	// Check if SpecValid condition changed
	specValidConditionChanged := common.HasConditionChanged(oldSpecValidCondition, newSpecValidCondition)

	if specValidConditionChanged {
		createEvent := func(c context.Context, e *domain.Event) { h.events.CreateEvent(c, orgId, e) }
		common.EmitSpecValidEvents(ctx, device, oldSpecValidCondition, newSpecValidCondition,
			createEvent, common.GetDeviceSpecValidEvent, common.GetDeviceSpecInvalidEvent,
			h.log)
	}
}

func (h *DeviceServiceHandler) OverwriteDeviceRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string, repositoryNames ...string) domain.Status {
	err := h.deviceStore.OverwriteRepositoryRefs(ctx, orgId, name, repositoryNames...)
	return common.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
}

func (h *DeviceServiceHandler) GetDeviceRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string) (*domain.RepositoryList, domain.Status) {
	result, err := h.deviceStore.GetRepositoryRefs(ctx, orgId, name)
	return result, common.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
}

func (h *DeviceServiceHandler) CountDevices(ctx context.Context, orgId uuid.UUID, params domain.ListDevicesParams, annotationSelector *selector.AnnotationSelector) (int64, domain.Status) {
	storeParams, status := convertDeviceListParams(params, annotationSelector)
	if status.Code != http.StatusOK {
		return 0, status
	}
	result, err := h.deviceStore.Count(ctx, orgId, storeParams.ListParams)
	return result, common.StoreErrorToApiStatus(err, false, domain.DeviceKind, nil)
}

func (h *DeviceServiceHandler) UnmarkDevicesRolloutSelection(ctx context.Context, orgId uuid.UUID, fleetName string) domain.Status {
	err := h.deviceStore.UnmarkRolloutSelection(ctx, orgId, fleetName)
	return common.StoreErrorToApiStatus(err, false, domain.DeviceKind, nil)
}

func (h *DeviceServiceHandler) MarkDevicesRolloutSelection(ctx context.Context, orgId uuid.UUID, params domain.ListDevicesParams, annotationSelector *selector.AnnotationSelector, limit *int) domain.Status {
	storeParams, status := convertDeviceListParams(params, annotationSelector)
	if status.Code != http.StatusOK {
		return status
	}
	err := h.deviceStore.MarkRolloutSelection(ctx, orgId, storeParams.ListParams, limit)
	return common.StoreErrorToApiStatus(err, false, domain.DeviceKind, nil)
}

func (h *DeviceServiceHandler) GetDeviceCompletionCounts(ctx context.Context, orgId uuid.UUID, owner string, templateVersion string, updateTimeout *time.Duration) ([]domain.DeviceCompletionCount, domain.Status) {
	result, err := h.deviceStore.CompletionCounts(ctx, orgId, owner, templateVersion, updateTimeout)
	return result, common.StoreErrorToApiStatus(err, false, domain.DeviceKind, nil)
}

func (h *DeviceServiceHandler) CountDevicesByLabels(ctx context.Context, orgId uuid.UUID, params domain.ListDevicesParams, annotationSelector *selector.AnnotationSelector, groupBy []string) ([]map[string]any, domain.Status) {
	storeParams, status := convertDeviceListParams(params, annotationSelector)
	if status.Code != http.StatusOK {
		return nil, status
	}
	result, err := h.deviceStore.CountByLabels(ctx, orgId, storeParams.ListParams, groupBy)
	return result, common.StoreErrorToApiStatus(err, false, domain.DeviceKind, nil)
}

func (h *DeviceServiceHandler) GetDevicesSummary(ctx context.Context, orgId uuid.UUID, params domain.ListDevicesParams, annotationSelector *selector.AnnotationSelector) (*domain.DevicesSummary, domain.Status) {
	storeParams, status := convertDeviceListParams(params, annotationSelector)
	if status.Code != http.StatusOK {
		return nil, status
	}
	result, err := h.deviceStore.Summary(ctx, orgId, storeParams.ListParams)
	return result, common.StoreErrorToApiStatus(err, false, domain.DeviceKind, nil)
}

func (h *DeviceServiceHandler) UpdateServiceSideDeviceStatus(ctx context.Context, orgId uuid.UUID, device domain.Device) bool {
	anyChanged := common.UpdateServiceSideStatus(ctx, orgId, &device, h.fleetStore, h.log)
	return anyChanged
}

func (h *DeviceServiceHandler) ResumeDevices(ctx context.Context, orgId uuid.UUID, request domain.DeviceResumeRequest) (domain.DeviceResumeResponse, domain.Status) {
	h.log.Infof("ResumeDevices called with label selector: %v, field selector: %v",
		request.LabelSelector, request.FieldSelector)

	// Create list params with both label and field selectors
	listParams, status := common.PrepareListParams(nil, request.LabelSelector, request.FieldSelector, nil)
	if status.Code != http.StatusOK {
		return domain.DeviceResumeResponse{}, status
	}

	// Remove conflictPaused annotation from all matching devices in a single SQL query
	resumedCount, deviceIDs, err := h.deviceStore.RemoveConflictPausedAnnotation(ctx, orgId, lo.FromPtr(listParams))
	if err != nil {
		var se *selector.SelectorError
		switch {
		case selector.AsSelectorError(err, &se):
			return domain.DeviceResumeResponse{}, domain.StatusBadRequest(se.Error())
		default:
			return domain.DeviceResumeResponse{}, domain.StatusInternalServerError(fmt.Sprintf("failed to resume devices: %v", err))
		}
	}

	h.log.Infof("Resumed %d devices: %v", resumedCount, deviceIDs)

	// Emit DeviceConflictResolved events for each resumed device
	if h.events != nil {
		for _, deviceID := range deviceIDs {
			event := common.GetDeviceConflictResolvedEvent(ctx, deviceID)
			h.events.CreateEvent(ctx, orgId, event)
		}
		h.log.Infof("Created DeviceConflictResolved events for %d devices", len(deviceIDs))
	}

	return domain.DeviceResumeResponse{
		ResumedDevices: int(resumedCount),
	}, domain.StatusOK()
}

// ListLabels only ever supports domain.DeviceKind (its monolith implementation never handled
// any other kind), so it moves here verbatim rather than to a new cross-resource home.
func (h *DeviceServiceHandler) ListLabels(ctx context.Context, orgId uuid.UUID, params domain.ListLabelsParams) (*domain.LabelList, domain.Status) {
	var err error

	kind := params.Kind

	listParams, status := common.PrepareListParams(nil, params.LabelSelector, params.FieldSelector, params.Limit)
	if status != domain.StatusOK() {
		return nil, status
	}

	var result domain.LabelList
	switch kind {
	case domain.DeviceKind:
		result, err = h.deviceStore.Labels(ctx, orgId, *listParams)
	default:
		return nil, domain.StatusBadRequest(fmt.Sprintf("unsupported kind: %s", kind))
	}

	if err == nil {
		return &result, domain.StatusOK()
	}

	var se *selector.SelectorError

	switch {
	case selector.AsSelectorError(err, &se):
		return nil, domain.StatusBadRequest(se.Error())
	default:
		return nil, domain.StatusInternalServerError(err.Error())
	}
}

// callbackDeviceUpdated is the device-specific callback that handles device events
func (h *DeviceServiceHandler) callbackDeviceUpdated(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	EmitDeviceUpdatedEvent(ctx, h.events, h.log, resourceKind, orgId, name, oldResource, newResource, created, err)
}

// callbackDeviceDecommission is the device-specific callback that handles device decommission events
func (h *DeviceServiceHandler) callbackDeviceDecommission(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	EmitDeviceDecommissionEvent(ctx, h.events, resourceKind, orgId, name, created, err)
}

// callbackDeviceDeleted is the device-specific callback that handles device deletion events
func (h *DeviceServiceHandler) callbackDeviceDeleted(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	h.events.HandleGenericResourceDeletedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
}

// processAwaitingReconnectIfNeeded processes the awaiting reconnect annotation only if the KV store contains the awaiting reconnection key.
// Returns true if the annotation was processed (regardless of whether the device ended up ConflictPaused or Online).
func (h *DeviceServiceHandler) processAwaitingReconnectIfNeeded(ctx context.Context, orgId uuid.UUID, deviceName string, deviceReportedVersion *string) bool {
	// Check if KV store contains the awaiting reconnection key
	key := kvstore.AwaitingReconnectionKey{
		OrgID:      orgId,
		DeviceName: deviceName,
	}
	keyStr := key.ComposeKey()
	kvValue, err := h.kvStore.Get(ctx, keyStr)
	if err != nil {
		h.log.WithError(err).Warnf("failed to check awaiting reconnection key for device %s", deviceName)
		// Don't fail the request, just log the warning
		return false
	}

	if kvValue != nil && string(kvValue) == "true" {
		versionStr := "nil"
		if deviceReportedVersion != nil {
			versionStr = *deviceReportedVersion
		}
		h.log.Infof("Processing awaiting reconnect annotation for device %s (orgId: %s, version: %s)", deviceName, orgId, versionStr)
		// Only process the annotation if the KV store contains the key with value "true"
		wasConflictPaused, err := h.deviceStore.ProcessAwaitingReconnectAnnotation(ctx, orgId, deviceName, deviceReportedVersion)
		if err != nil {
			h.log.WithError(err).Warnf("failed to process awaiting reconnect annotation for device %s", deviceName)
			// Don't fail the request, just log the warning
			return false
		}
		h.log.Infof("Successfully processed awaiting reconnect annotation for device %s, wasConflictPaused: %t", deviceName, wasConflictPaused)
		// Successfully processed the annotation, now remove the key from KV store
		if err := h.kvStore.DeleteKeysForTemplateVersion(ctx, keyStr); err != nil {
			h.log.WithError(err).Warnf("failed to remove awaiting reconnection key for device %s", deviceName)
			// Don't fail the request, just log the warning
		} else {
			h.log.Infof("Successfully removed awaiting reconnection key for device %s", deviceName)
		}

		// Create event if device was moved to conflict paused state
		if wasConflictPaused && h.events != nil {
			h.log.Infof("Device %s was moved to conflict paused state, creating event", deviceName)
			event := common.GetDeviceConflictPausedEvent(ctx, deviceName)
			if event != nil {
				h.events.CreateEvent(ctx, orgId, event)
				h.log.Infof("Successfully created conflict paused event for device %s", deviceName)
			} else {
				h.log.Warnf("Failed to create conflict paused event for device %s - event is nil", deviceName)
			}
		}
		return true
	}
	h.log.Debugf("Skipping awaiting reconnect annotation processing for device %s - KV value is not 'true' (value: %s)", deviceName, string(kvValue))
	return false
}
