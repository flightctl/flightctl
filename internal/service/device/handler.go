package device

import (
	"context"
	"errors"
	"fmt"
	"maps"
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
	catalogstore "github.com/flightctl/flightctl/internal/store/catalog"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/util/validation"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/semaphore"
)

// DeviceServiceHandler implements Service.
type DeviceServiceHandler struct {
	deviceStore       devicestore.Store
	catalogStore      catalogstore.Store
	fleetStore        fleetstore.Store
	events            events.Service
	kvStore           kvstore.KVStore
	agentGate         *semaphore.Weighted
	agentEndpoint     string
	log               logrus.FieldLogger
	callEventCallback store.EventCallbackCaller
}

// NewDeviceServiceHandler creates a new DeviceServiceHandler instance.
// catalogStore is optional — when nil, catalog item ref validation is skipped.
func NewDeviceServiceHandler(
	deviceStore devicestore.Store,
	catalogStore catalogstore.Store,
	fleetStore fleetstore.Store,
	events events.Service,
	kvStore kvstore.KVStore,
	agentEndpoint string,
	log logrus.FieldLogger,
) Service {
	return &DeviceServiceHandler{
		deviceStore:       deviceStore,
		catalogStore:      catalogStore,
		fleetStore:        fleetStore,
		events:            events,
		kvStore:           kvStore,
		agentGate:         semaphore.NewWeighted(common.MaxConcurrentAgents),
		agentEndpoint:     agentEndpoint,
		log:               log,
		callEventCallback: store.CallEventCallback(domain.DeviceKind, log),
	}
}

var _ Service = (*DeviceServiceHandler)(nil)

func (h *DeviceServiceHandler) HealthcheckDevices(ctx context.Context, orgId uuid.UUID, names []string) error {
	return h.deviceStore.Healthcheck(ctx, orgId, names)
}

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
func ReplaceDeviceFromUntrusted(ctx context.Context, svc Service, orgId uuid.UUID, name string, device domain.Device, fieldsToUnset []string, enforceOwnership bool, enforceCapabilities bool) (*domain.Device, domain.Status) {
	SanitizeDevice(&device)
	return svc.ReplaceDevice(ctx, orgId, name, device, fieldsToUnset, enforceOwnership, enforceCapabilities)
}

func (h *DeviceServiceHandler) CreateDevice(ctx context.Context, orgId uuid.UUID, device domain.Device) (*domain.Device, domain.Status) {
	if device.Spec != nil && device.Spec.Decommissioning != nil {
		h.log.WithError(flterrors.ErrDecommission).Error("attempt to create decommissioned device")
		return nil, domain.StatusBadRequest(flterrors.ErrDecommission.Error())
	}

	if errs := device.Validate(); len(errs) > 0 {
		return nil, domain.StatusBadRequest(errors.Join(errs...).Error())
	}

	if status := common.ValidateCatalogItemRefs(ctx, orgId, h.catalogStore, device.Spec); status != domain.StatusOK() {
		return nil, status
	}

	_ = common.UpdateServiceSideStatus(ctx, orgId, &device, h.fleetStore, h.log)

	name := lo.FromPtr(device.Metadata.Name)
	result, err := h.deviceStore.Create(ctx, orgId, &device, nil)
	h.callEventCallback(ctx, h.callbackDeviceUpdated, orgId, name, nil, result, true, err)
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

func (h *DeviceServiceHandler) ReplaceDevice(ctx context.Context, orgId uuid.UUID, name string, device domain.Device, fieldsToUnset []string, enforceOwnership bool, enforceCapabilities bool) (*domain.Device, domain.Status) {
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

	if status := common.ValidateCatalogItemRefs(ctx, orgId, h.catalogStore, device.Spec); status != domain.StatusOK() {
		return nil, status
	}

	result, before, created, err := h.deviceStore.Mutate(ctx, orgId, name, nil, func(m *devicestore.DeviceMutation) error {
		creating := m.Device == nil
		if creating {
			status := domain.NewDeviceStatus()
			m.Device = &domain.Device{
				ApiVersion: domain.DeviceAPIVersion,
				Kind:       domain.DeviceKind,
				Metadata:   device.Metadata,
				Spec:       device.Spec,
				Status:     &status,
			}
			m.Device.Metadata.Name = lo.ToPtr(name)
			// Intentional: brand-new devices have no LastSeen yet, so USSS leaves
			// summary/applications as Unknown until the agent first reports in.
			_ = common.UpdateServiceSideStatus(ctx, orgId, m.Device, h.fleetStore, h.log)
			return pruneLifecycleOnCurrent(h.log, m.Device)
		}
		current := m.Device
		if err := rejectDecommissionedDevice(current); err != nil {
			return err
		}
		if err := common.CheckResourceVersionConflict(&current.Metadata, &device.Metadata); err != nil {
			return err
		}
		if enforceOwnership && len(lo.FromPtr(current.Metadata.Owner)) != 0 && !domain.DeviceSpecsAreEqual(lo.FromPtr(current.Spec), lo.FromPtr(device.Spec)) {
			return flterrors.ErrUpdatingResourceWithOwnerNotAllowed
		}
		if enforceCapabilities && isPackageModeOsTargetConflict(current, &device) {
			return flterrors.ErrOsTargetNotSupportedOnPackageMode
		}
		if device.Spec != nil {
			current.Spec = device.Spec
		}
		if device.Metadata.Labels != nil || lo.Contains(fieldsToUnset, "labels") {
			current.Metadata.Labels = device.Metadata.Labels
		}
		if device.Metadata.Annotations != nil || lo.Contains(fieldsToUnset, "annotations") {
			current.Metadata.Annotations = device.Metadata.Annotations
		}
		if device.Metadata.Owner != nil || lo.Contains(fieldsToUnset, "owner") {
			current.Metadata.Owner = device.Metadata.Owner
		}
		_ = common.UpdateServiceSideStatus(ctx, orgId, current, h.fleetStore, h.log)
		return pruneLifecycleOnCurrent(h.log, current)
	}, devicestore.WithTimestamp())
	h.callEventCallback(ctx, h.callbackDeviceUpdated, orgId, name, before, result, created, err)
	return result, common.StoreErrorToApiStatus(err, created, domain.DeviceKind, &name)
}

// ReplaceDeviceSpec is the internal-reconciler counterpart to ReplaceDevice: it overwrites
// Spec and merges setAnnotations/deleteAnnotations into the existing annotation map, without
// requiring the caller's resourceVersion to match. Ownership is re-verified against the
// freshly-loaded device on every retry attempt (not a stale caller-held snapshot), so a
// concurrent owner change is still caught. Labels are never touched.
func (h *DeviceServiceHandler) ReplaceDeviceSpec(ctx context.Context, orgId uuid.UUID, name string, expectedOwner *string, spec domain.DeviceSpec, setAnnotations map[string]string, deleteAnnotations []string) (*domain.Device, domain.Status) {
	result, before, _, err := h.deviceStore.Mutate(ctx, orgId, name, nil, func(m *devicestore.DeviceMutation) error {
		if err := m.RequireExisting(); err != nil {
			return err
		}
		current := m.Device
		if err := rejectDecommissionedDevice(current); err != nil {
			return err
		}
		if util.DefaultIfNil(current.Metadata.Owner, "") != util.DefaultIfNil(expectedOwner, "") {
			return flterrors.ErrUpdatingResourceWithOwnerNotAllowed
		}
		current.Spec = &spec
		ann := util.EnsureMap(lo.FromPtr(current.Metadata.Annotations))
		for k, v := range setAnnotations {
			ann[k] = v
		}
		for _, k := range deleteAnnotations {
			delete(ann, k)
		}
		current.Metadata.Annotations = &ann
		_ = common.UpdateServiceSideStatus(ctx, orgId, current, h.fleetStore, h.log)
		return pruneLifecycleOnCurrent(h.log, current)
	}, devicestore.WithTimestamp())
	h.callEventCallback(ctx, h.callbackDeviceUpdated, orgId, name, before, result, false, err)
	return result, common.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
}

// SetDeviceOwner sets (newOwner non-nil) or clears (newOwner nil) only Owner, re-verified
// against the freshly-loaded device's owner on every retry attempt. Spec/Labels/Annotations
// are never part of the write payload, so this cannot race with heartbeats or clobber a
// concurrently-rendered spec the way a full ReplaceDevice call would.
func (h *DeviceServiceHandler) SetDeviceOwner(ctx context.Context, orgId uuid.UUID, name string, expectedOwner *string, newOwner *string) (*domain.Device, domain.Status) {
	result, before, _, err := h.deviceStore.Mutate(ctx, orgId, name, nil, func(m *devicestore.DeviceMutation) error {
		if err := m.RequireExisting(); err != nil {
			return err
		}
		current := m.Device
		if err := rejectDecommissionedDevice(current); err != nil {
			return err
		}
		if util.DefaultIfNil(current.Metadata.Owner, "") != util.DefaultIfNil(expectedOwner, "") {
			return flterrors.ErrUpdatingResourceWithOwnerNotAllowed
		}
		current.Metadata.Owner = newOwner
		return nil
	})
	h.callEventCallback(ctx, h.callbackDeviceUpdated, orgId, name, before, result, false, err)
	return result, common.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
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

	// Ownership is never enforced on UpdateDevice (agent/console trusted path).
	result, before, _, err := h.deviceStore.Mutate(ctx, orgId, name, nil, func(m *devicestore.DeviceMutation) error {
		if err := m.RequireExisting(); err != nil {
			return err
		}
		current := m.Device
		if err := rejectDecommissionedDevice(current); err != nil {
			return err
		}
		if device.Spec != nil {
			current.Spec = device.Spec
		}
		if device.Metadata.Labels != nil || lo.Contains(fieldsToUnset, "labels") {
			current.Metadata.Labels = device.Metadata.Labels
		}
		if device.Metadata.Annotations != nil || lo.Contains(fieldsToUnset, "annotations") {
			current.Metadata.Annotations = device.Metadata.Annotations
		}
		if device.Metadata.Owner != nil || lo.Contains(fieldsToUnset, "owner") {
			current.Metadata.Owner = device.Metadata.Owner
		}
		_ = common.UpdateServiceSideStatus(ctx, orgId, current, h.fleetStore, h.log)
		return pruneLifecycleOnCurrent(h.log, current)
	}, devicestore.WithTimestamp())
	h.callEventCallback(ctx, h.callbackDeviceUpdated, orgId, name, before, result, false, err)
	return result, err
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

	result, before, err := h.deviceStore.UpdateStatus(ctx, orgId, deviceToStore, originalDevice)
	h.callEventCallback(ctx, h.callbackDeviceUpdated, orgId, name, before, result, false, err)
	return result, common.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
}

func (h *DeviceServiceHandler) PatchDeviceStatus(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest) (*domain.Device, domain.Status) {
	currentObj, err := h.deviceStore.Get(ctx, orgId, name)
	if err != nil {
		return nil, common.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
	}

	// Fast-fail validation against the pre-mutate snapshot (400s). The Mutate callback
	// reapplies the patch against the CAS-fresh current on every attempt.
	if status := validateDeviceStatusPatch(ctx, currentObj, patch, name); status.Code != http.StatusOK {
		return nil, status
	}

	var callbackStatus domain.Status
	result, before, _, err := h.deviceStore.Mutate(ctx, orgId, name, currentObj, func(m *devicestore.DeviceMutation) error {
		if err := m.RequireExisting(); err != nil {
			return err
		}
		if err := rejectDecommissionedDevice(m.Device); err != nil {
			return err
		}
		patched, err := applyDeviceStatusPatch(ctx, m.Device, patch, name)
		if err != nil {
			callbackStatus = domain.StatusBadRequest(err.Error())
			return err
		}
		m.Device.Status = patched.Status
		_ = common.UpdateServiceSideStatus(ctx, orgId, m.Device, h.fleetStore, h.log)
		return nil
	}, devicestore.WithTimestamp())
	h.callEventCallback(ctx, h.callbackDeviceUpdated, orgId, name, before, result, false, err)
	if err != nil && callbackStatus.Code != 0 {
		return result, callbackStatus
	}
	return result, common.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
}

func validateDeviceStatusPatch(ctx context.Context, current *domain.Device, patch domain.PatchRequest, name string) domain.Status {
	_, err := applyDeviceStatusPatch(ctx, current, patch, name)
	if err != nil {
		return domain.StatusBadRequest(err.Error())
	}
	return domain.StatusOK()
}

func applyDeviceStatusPatch(ctx context.Context, current *domain.Device, patch domain.PatchRequest, name string) (*domain.Device, error) {
	patched := &domain.Device{}
	if err := common.ApplyJSONPatch(ctx, current, patched, patch, "/devices/"+name); err != nil {
		return nil, err
	}
	if errs := validateDeviceStatus(patched); len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if errs := patched.Validate(); len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if !reflect.DeepEqual(patched.Metadata, current.Metadata) {
		return nil, errors.New("metadata is immutable")
	}
	if current.ApiVersion != patched.ApiVersion {
		return nil, errors.New("apiVersion is immutable")
	}
	if current.Kind != patched.Kind {
		return nil, errors.New("kind is immutable")
	}
	if !reflect.DeepEqual(current.Spec, patched.Spec) {
		return nil, errors.New("spec is immutable")
	}
	common.NilOutManagedObjectMetaProperties(&patched.Metadata)
	patched.Metadata.ResourceVersion = nil
	return patched, nil
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
func (h *DeviceServiceHandler) PatchDevice(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest, enforceOwnership bool, enforceCapabilities bool) (*domain.Device, domain.Status) {
	currentObj, err := h.deviceStore.Get(ctx, orgId, name)
	if err != nil {
		return nil, common.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
	}

	// Fast-fail validation against the pre-mutate snapshot (400s). The Mutate callback reapplies
	// the patch against the CAS-fresh current on every attempt so ownership/spec checks stay correct.
	if status := validateDevicePatch(ctx, orgId, h.catalogStore, currentObj, patch, name); status.Code != http.StatusOK {
		return nil, status
	}

	var callbackStatus domain.Status
	result, before, _, err := h.deviceStore.Mutate(ctx, orgId, name, currentObj, func(m *devicestore.DeviceMutation) error {
		if err := m.RequireExisting(); err != nil {
			return err
		}
		current := m.Device
		if err := rejectDecommissionedDevice(current); err != nil {
			return err
		}
		patched, err := applyDevicePatch(ctx, current, patch, name)
		if err != nil {
			callbackStatus = domain.StatusBadRequest(err.Error())
			return err
		}
		if status := common.ValidateCatalogItemRefs(ctx, orgId, h.catalogStore, patched.Spec); status != domain.StatusOK() {
			callbackStatus = status
			return common.ApiStatusToErr(status)
		}
		if enforceOwnership && len(lo.FromPtr(current.Metadata.Owner)) != 0 && !domain.DeviceSpecsAreEqual(lo.FromPtr(current.Spec), lo.FromPtr(patched.Spec)) {
			return flterrors.ErrUpdatingResourceWithOwnerNotAllowed
		}
		if enforceCapabilities && isPackageModeOsTargetConflict(current, patched) {
			return flterrors.ErrOsTargetNotSupportedOnPackageMode
		}

		current.Spec = patched.Spec
		current.Metadata.Labels = patched.Metadata.Labels
		if patched.Status != nil {
			current.Status = patched.Status
		}
		_ = common.UpdateServiceSideStatus(ctx, orgId, current, h.fleetStore, h.log)
		return pruneLifecycleOnCurrent(h.log, current)
	}, devicestore.WithTimestamp())
	h.callEventCallback(ctx, h.callbackDeviceUpdated, orgId, name, before, result, false, err)
	if err != nil && callbackStatus.Code != 0 {
		return result, callbackStatus
	}
	return result, common.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
}

func validateDevicePatch(ctx context.Context, orgId uuid.UUID, catalogStore catalogstore.Store, current *domain.Device, patch domain.PatchRequest, name string) domain.Status {
	patched, err := applyDevicePatch(ctx, current, patch, name)
	if err != nil {
		return domain.StatusBadRequest(err.Error())
	}
	return common.ValidateCatalogItemRefs(ctx, orgId, catalogStore, patched.Spec)
}

func applyDevicePatch(ctx context.Context, current *domain.Device, patch domain.PatchRequest, name string) (*domain.Device, error) {
	patched := &domain.Device{}
	if err := common.ApplyJSONPatch(ctx, current, patched, patch, "/devices/"+name); err != nil {
		return nil, err
	}
	if current.Status != nil && patched.Status != nil {
		patched.Status.LastSeen = current.Status.LastSeen
		patched.Status.SystemInfo.AdditionalProperties = current.Status.SystemInfo.AdditionalProperties
	}
	if errs := patched.Validate(); len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if errs := current.ValidateUpdate(patched); len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if patched.Spec != nil && patched.Spec.Decommissioning != nil {
		return nil, errors.New("spec.decommissioning cannot be changed via patch request")
	}
	common.NilOutManagedObjectMetaProperties(&patched.Metadata)
	patched.Metadata.ResourceVersion = nil
	return patched, nil
}

func (h *DeviceServiceHandler) SetOutOfDate(ctx context.Context, orgId uuid.UUID, owner string) error {
	return h.deviceStore.SetOutOfDate(ctx, orgId, owner)
}

func (h *DeviceServiceHandler) UpdateServerSideDeviceStatus(ctx context.Context, orgId uuid.UUID, name string) error {
	device, err := h.deviceStore.GetWithTimestamp(ctx, orgId, name)
	if err != nil {
		return err
	}
	previous := snapshotDeviceForStatusUpdate(device)
	if changed := common.UpdateServiceSideStatus(ctx, orgId, device, h.fleetStore, h.log); changed {
		result, before, err := h.deviceStore.UpdateStatus(ctx, orgId, device, previous)
		h.callEventCallback(ctx, h.callbackDeviceUpdated, orgId, name, before, result, false, err)
		if err != nil {
			h.log.WithError(err).Errorf("failed to update status for device %s/%s", orgId, name)
			return err
		}
	}
	return nil
}

func (h *DeviceServiceHandler) ForceUpdateServerSideDeviceStatus(ctx context.Context, orgId uuid.UUID, name string) error {
	device, err := h.deviceStore.GetWithTimestamp(ctx, orgId, name)
	if err != nil {
		return err
	}
	previous := snapshotDeviceForStatusUpdate(device)
	common.UpdateServiceSideStatus(ctx, orgId, device, h.fleetStore, h.log)
	result, before, err := h.deviceStore.UpdateStatus(ctx, orgId, device, previous)
	h.callEventCallback(ctx, h.callbackDeviceUpdated, orgId, name, before, result, false, err)
	if err != nil {
		h.log.WithError(err).Errorf("failed to update status for device %s/%s", orgId, name)
		return err
	}
	return nil
}

// snapshotDeviceForStatusUpdate deep-copies metadata maps and status so later
// in-place mutations (annotations, conditions, USSS) do not alter event baselines.
func snapshotDeviceForStatusUpdate(device *domain.Device) *domain.Device {
	if device == nil {
		return nil
	}
	previous := *device
	if device.Metadata.Annotations != nil {
		ann := maps.Clone(*device.Metadata.Annotations)
		previous.Metadata.Annotations = &ann
	}
	if device.Metadata.Labels != nil {
		labels := maps.Clone(*device.Metadata.Labels)
		previous.Metadata.Labels = &labels
	}
	if device.Status != nil {
		status := *device.Status
		if device.Status.Conditions != nil {
			status.Conditions = append([]domain.Condition(nil), device.Status.Conditions...)
		}
		// LastSeen is a pointer; copy the value so later writes to Status.LastSeen
		// on the live device do not mutate the event baseline snapshot.
		if device.Status.LastSeen != nil {
			ls := *device.Status.LastSeen
			status.LastSeen = &ls
		}
		previous.Status = &status
	}
	return &previous
}

func (h *DeviceServiceHandler) DecommissionDevice(ctx context.Context, orgId uuid.UUID, name string, decom domain.DeviceDecommission) (*domain.Device, domain.Status) {
	result, before, _, err := h.deviceStore.Mutate(ctx, orgId, name, nil, func(m *devicestore.DeviceMutation) error {
		if err := m.RequireExisting(); err != nil {
			return err
		}
		// Product rule: refuse a second decommission without emitting a success event.
		// Map ErrDecommission → ErrResourceVersionConflict to preserve the historical API.
		if err := rejectDecommissionedDevice(m.Device); err != nil {
			return flterrors.ErrResourceVersionConflict
		}
		applyDeviceDecommission(m.Device, decom)
		// Former store DecommissionDevice did not bump generation; keep that contract.
		m.PreserveGeneration = true
		return nil
	})
	if err != nil {
		return nil, common.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
	}
	h.callEventCallback(ctx, h.callbackDeviceDecommission, orgId, name, before, result, false, nil)
	return result, common.StoreErrorToApiStatus(nil, false, domain.DeviceKind, &name)
}

// applyDeviceDecommission sets Spec.Decommissioning, Lifecycle Decommissioning, and clears Owner/Labels.
func applyDeviceDecommission(device *domain.Device, decom domain.DeviceDecommission) {
	spec := domain.DeviceSpec{}
	if device.Spec != nil {
		spec = *device.Spec
	}
	spec.Decommissioning = &decom
	device.Spec = &spec

	status := domain.NewDeviceStatus()
	if device.Status != nil {
		status = *device.Status
	}
	status.Lifecycle.Status = domain.DeviceLifecycleStatusDecommissioning
	device.Status = &status

	device.Metadata.Owner = nil
	device.Metadata.Labels = nil
}

func (h *DeviceServiceHandler) UpdateDeviceAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) domain.Status {
	err := h.deviceStore.UpdateAnnotations(ctx, orgId, name, annotations, deleteKeys)
	return common.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
}

func (h *DeviceServiceHandler) UpdateRenderedDevice(ctx context.Context, orgId uuid.UUID, name, renderedConfig, renderedApplications, specHash, osImage string, configFingerprints []domain.DependencySyncConfigRefStatus, forceUpdate bool) domain.Status {
	specValid := domain.Condition{
		Type:   domain.ConditionTypeDeviceSpecValid,
		Status: domain.ConditionStatusTrue,
		Reason: "Valid",
	}
	var previous, updated *domain.Device
	var oldConditions []domain.Condition
	var renderedVersion string
	_, _, _, err := h.deviceStore.Mutate(ctx, orgId, name, nil, func(m *devicestore.DeviceMutation) error {
		if err := m.RequireExisting(); err != nil {
			return err
		}
		// Capture pre-render conditions before applyRenderedUpdate sets SpecValid.
		previous = snapshotDeviceForStatusUpdate(m.Device)
		oldConditions = nil
		updated = nil
		if m.Device.Status != nil {
			oldConditions = append([]domain.Condition(nil), m.Device.Status.Conditions...)
		}
		version, err := applyRenderedUpdate(m, renderedConfig, renderedApplications, specHash, osImage, configFingerprints, forceUpdate)
		if err != nil {
			return err
		}
		renderedVersion = version
		var statusBefore *domain.DeviceStatus
		if m.Device.Status != nil {
			st := *m.Device.Status
			if m.Device.Status.Conditions != nil {
				st.Conditions = append([]domain.Condition(nil), m.Device.Status.Conditions...)
			}
			statusBefore = &st
		}
		if !common.UpdateServiceSideStatus(ctx, orgId, m.Device, h.fleetStore, h.log) {
			m.Device.Status = statusBefore
		}
		// Always emit the update when renderedVersion advanced, even if USSS made no status change.
		updated = m.Device
		return nil
	}, devicestore.WithTimestamp())
	if err != nil {
		h.log.Errorf("Failed to update rendered device %s/%s: %v", orgId, name, err)
		return common.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
	}
	if renderedVersion == "" {
		h.log.Debugf("Rendered device %s/%s: no change in rendered version", orgId, name)
		status := h.SetDeviceServiceConditions(ctx, orgId, name, []domain.Condition{specValid})
		if status.Code != http.StatusOK {
			return status
		}
		if err := h.UpdateServerSideDeviceStatus(ctx, orgId, name); err != nil {
			h.log.Errorf("Failed updating device status for device %s/%s: %v", orgId, name, err)
		}
		return status
	}
	if updated != nil {
		h.callbackDeviceUpdated(ctx, domain.DeviceKind, orgId, name, previous, updated, false, nil)
	}
	deviceForEvents := updated
	if deviceForEvents == nil {
		deviceForEvents = previous
	}
	if deviceForEvents != nil {
		newConditions := append([]domain.Condition(nil), oldConditions...)
		domain.SetStatusCondition(&newConditions, specValid)
		h.diffAndEmitConditionEvents(ctx, orgId, deviceForEvents, oldConditions, newConditions)
	}

	if err := rendered.Bus.Instance().StoreAndNotify(ctx, orgId, name, renderedVersion); err != nil {
		h.log.Errorf("Failed to publish rendered device %s/%s: %v", orgId, name, err)
	}
	return domain.StatusOK()
}

func (h *DeviceServiceHandler) SetDeviceServiceConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []domain.Condition) domain.Status {
	var oldConditions, newConditions []domain.Condition
	result, _, _, err := h.deviceStore.Mutate(ctx, orgId, name, nil, func(m *devicestore.DeviceMutation) error {
		if err := m.RequireExisting(); err != nil {
			return err
		}
		existing := serviceConditionsFromDevice(m.Device)
		merged, changed := common.MergeStatusConditions(existing, conditions)
		if !changed {
			return store.ErrMutateSkipWrite
		}
		oldConditions = existing
		newConditions = merged
		replaceServiceConditionsOnDevice(m.Device, merged)
		return nil
	})
	if err != nil {
		return common.StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
	}
	if result != nil {
		h.diffAndEmitConditionEvents(ctx, orgId, result, oldConditions, newConditions)
	}
	return domain.StatusOK()
}

// serviceConditionsFromDevice returns SpecValid / MultipleOwners conditions from
// Status.Conditions (agent and service conditions are stored separately but
// exposed together by Get).
func serviceConditionsFromDevice(device *domain.Device) []domain.Condition {
	if device == nil || device.Status == nil {
		return nil
	}
	var out []domain.Condition
	for _, c := range device.Status.Conditions {
		if c.Type.IsServiceConditionType() {
			out = append(out, c)
		}
	}
	return out
}

// replaceServiceConditionsOnDevice swaps service-owned conditions on Status while
// preserving agent conditions. NewDeviceFromApiResource routes service types into
// the service_conditions column on persist.
func replaceServiceConditionsOnDevice(device *domain.Device, serviceConds []domain.Condition) {
	if device.Status == nil {
		status := domain.NewDeviceStatus()
		device.Status = &status
	}
	var agent []domain.Condition
	for _, c := range device.Status.Conditions {
		if !c.Type.IsServiceConditionType() {
			agent = append(agent, c)
		}
	}
	device.Status.Conditions = append(agent, serviceConds...)
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

		var wasConflictPaused bool
		err := common.RetryOnNoRowsUpdated(func() error {
			device, getErr := h.deviceStore.Get(ctx, orgId, deviceName)
			if getErr != nil {
				return getErr
			}
			apply, outcome := decideAwaitingReconnect(device, deviceReportedVersion)
			if !apply {
				wasConflictPaused = false
				return nil
			}
			if applyErr := h.deviceStore.ApplyAwaitingReconnectOutcome(ctx, orgId, deviceName, outcome); applyErr != nil {
				return applyErr
			}
			wasConflictPaused = outcome.ConflictPaused
			return nil
		})
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

// isPackageModeOsTargetConflict reports whether the incoming device spec newly assigns
// or changes an OS target (image or catalogItemRef) on a package-mode device.
// Clearing an OS target (nil/empty incoming) is not a conflict — that is the
// remediation path for a stuck package-mode device. Unrelated updates that retain
// the existing OS target are also not conflicts.
func isPackageModeOsTargetConflict(existing *domain.Device, incoming *domain.Device) bool {
	if existing.Status == nil || existing.Status.Capabilities == nil || existing.Status.Capabilities.OsMode == nil {
		return false
	}
	if *existing.Status.Capabilities.OsMode != domain.OsModePackage {
		return false
	}

	if incoming.Spec == nil || incoming.Spec.Os == nil {
		return false
	}
	incomingHasTarget := incoming.Spec.Os.Image != "" || incoming.Spec.Os.CatalogItemRef != nil
	if !incomingHasTarget {
		return false
	}

	var existingOs *domain.DeviceOsSpec
	if existing.Spec != nil {
		existingOs = existing.Spec.Os
	}
	if existingOs == nil {
		return true
	}

	if incoming.Spec.Os.Image != existingOs.Image {
		return true
	}
	if !reflect.DeepEqual(incoming.Spec.Os.CatalogItemRef, existingOs.CatalogItemRef) {
		return true
	}
	return false
}
