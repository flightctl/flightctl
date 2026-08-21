package common

import (
	"context"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
)

type stubFleetStore struct {
	fleetstore.Store
	fleet *domain.Fleet
}

func (s *stubFleetStore) Get(_ context.Context, _ uuid.UUID, _ string, _ ...fleetstore.GetOption) (*domain.Fleet, error) {
	return s.fleet, nil
}

func TestComputeDeviceStatusChanges_DeviceUpdateFailed(t *testing.T) {
	ctx := context.Background()
	orgId := uuid.New()

	// Create a device with an update error condition
	deviceWithError := &domain.Device{
		Metadata: domain.ObjectMeta{
			Name: lo.ToPtr("test-device"),
		},
		Status: &domain.DeviceStatus{
			Updated: domain.DeviceUpdatedStatus{
				Status: domain.DeviceUpdatedStatusOutOfDate,
				Info:   lo.ToPtr("Device could not be updated to the fleet's latest device spec: update failed"),
			},
			Conditions: []domain.Condition{
				{
					Type:    domain.ConditionTypeDeviceUpdating,
					Status:  domain.ConditionStatusFalse,
					Reason:  string(domain.UpdateStateError),
					Message: "update failed",
				},
			},
		},
	}

	// Create a device without an update error condition
	deviceWithoutError := &domain.Device{
		Metadata: domain.ObjectMeta{
			Name: lo.ToPtr("test-device"),
		},
		Status: &domain.DeviceStatus{
			Updated: domain.DeviceUpdatedStatus{
				Status: domain.DeviceUpdatedStatusOutOfDate,
				Info:   lo.ToPtr("Device has not been updated to the latest device spec."),
			},
		},
	}

	// Create an old device with UpToDate status for comparison
	oldDevice := &domain.Device{
		Metadata: domain.ObjectMeta{
			Name: lo.ToPtr("test-device"),
		},
		Status: &domain.DeviceStatus{
			Updated: domain.DeviceUpdatedStatus{
				Status: domain.DeviceUpdatedStatusUpToDate,
				Info:   lo.ToPtr("Device was updated to the latest device spec."),
			},
		},
	}

	// Test case 1: Device with update error should emit DeviceUpdateFailed event
	updates := ComputeDeviceStatusChanges(ctx, oldDevice, deviceWithError, orgId)
	assert.Len(t, updates, 1)
	assert.Equal(t, domain.EventReasonDeviceUpdateFailed, updates[0].Reason)
	assert.Contains(t, updates[0].Details, "update failed")

	// Test case 2: Device without update error should emit DeviceContentOutOfDate event
	updates = ComputeDeviceStatusChanges(ctx, oldDevice, deviceWithoutError, orgId)
	assert.Len(t, updates, 1)
	assert.Equal(t, domain.EventReasonDeviceContentOutOfDate, updates[0].Reason)
	assert.Contains(t, updates[0].Details, "has not been updated")
}

func TestComputeDeviceStatusChanges_OSImageChanged_EDM3986(t *testing.T) {
	ctx := context.Background()
	orgId := uuid.New()

	tests := []struct {
		name            string
		oldDigest       string
		newDigest       string
		expectEvent     bool
		expectedDetails string
	}{
		{
			name:            "When initial OS image is reported it should emit event with 'Initial OS image detected' message",
			oldDigest:       "",
			newDigest:       "sha256:abc123",
			expectEvent:     true,
			expectedDetails: "Initial OS image detected: sha256:abc123",
		},
		{
			name:            "When OS image changes it should emit event with from/to message",
			oldDigest:       "sha256:old111",
			newDigest:       "sha256:new222",
			expectEvent:     true,
			expectedDetails: "OS image changed from sha256:old111 to sha256:new222",
		},
		{
			name:        "When OS image is unchanged it should not emit event",
			oldDigest:   "sha256:same",
			newDigest:   "sha256:same",
			expectEvent: false,
		},
		{
			name:        "When new digest is empty it should not emit event",
			oldDigest:   "sha256:old",
			newDigest:   "",
			expectEvent: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldDevice := &domain.Device{
				Metadata: domain.ObjectMeta{Name: lo.ToPtr("test-device")},
				Status: &domain.DeviceStatus{
					Os: domain.DeviceOsStatus{ImageDigest: tt.oldDigest},
				},
			}
			newDevice := &domain.Device{
				Metadata: domain.ObjectMeta{Name: lo.ToPtr("test-device")},
				Status: &domain.DeviceStatus{
					Os: domain.DeviceOsStatus{ImageDigest: tt.newDigest},
				},
			}

			updates := ComputeDeviceStatusChanges(ctx, oldDevice, newDevice, orgId)

			var osImageEvents []ResourceUpdate
			for _, u := range updates {
				if u.Reason == domain.EventReasonDeviceOSImageChanged {
					osImageEvents = append(osImageEvents, u)
				}
			}

			if tt.expectEvent {
				assert.Len(t, osImageEvents, 1)
				assert.Equal(t, tt.expectedDetails, osImageEvents[0].Details)
			} else {
				assert.Empty(t, osImageEvents)
			}
		})
	}
}

func TestComputeDeviceStatusChanges_StatusTransition(t *testing.T) {
	ctx := context.Background()
	orgId := uuid.New()

	// Create old device with UpToDate status
	oldDevice := &domain.Device{
		Metadata: domain.ObjectMeta{
			Name: lo.ToPtr("test-device"),
		},
		Status: &domain.DeviceStatus{
			Updated: domain.DeviceUpdatedStatus{
				Status: domain.DeviceUpdatedStatusUpToDate,
				Info:   lo.ToPtr("Device was updated to the latest device spec."),
			},
		},
	}

	// Create new device with OutOfDate status and update error
	newDevice := &domain.Device{
		Metadata: domain.ObjectMeta{
			Name: lo.ToPtr("test-device"),
		},
		Status: &domain.DeviceStatus{
			Updated: domain.DeviceUpdatedStatus{
				Status: domain.DeviceUpdatedStatusOutOfDate,
				Info:   lo.ToPtr("Device could not be updated to the fleet's latest device spec: update failed"),
			},
			Conditions: []domain.Condition{
				{
					Type:    domain.ConditionTypeDeviceUpdating,
					Status:  domain.ConditionStatusFalse,
					Reason:  string(domain.UpdateStateError),
					Message: "update failed",
				},
			},
		},
	}

	// Test transition from UpToDate to OutOfDate with error
	updates := ComputeDeviceStatusChanges(ctx, oldDevice, newDevice, orgId)
	assert.Len(t, updates, 1)
	assert.Equal(t, domain.EventReasonDeviceUpdateFailed, updates[0].Reason)
	assert.Contains(t, updates[0].Details, "update failed")
}

func TestUpdateServerSideDeviceStatus_PostRestoreState(t *testing.T) {
	// This test validates the critical post-restore state where ALL three conditions must be true:
	// 1. awaitingReconnect annotation = "true"
	// 2. lastSeen = zero time (cleared by restore)
	// 3. status summary = AwaitingReconnect
	//
	// This ensures that after restore, the AwaitingReconnect status takes precedence over
	// disconnection logic (which would normally trigger due to zero lastSeen time)

	tests := []struct {
		name                    string
		hasAwaitingAnnotation   bool
		lastSeenTime            time.Time
		hasResourceErrors       bool
		hasResourceDegradations bool
		isRebooting             bool
		expectedStatus          domain.DeviceSummaryStatusType
		expectedInfo            string
	}{
		{
			name:                  "Post-restore state: annotation=true, lastSeen=zero, should be AwaitingReconnect",
			hasAwaitingAnnotation: true,
			lastSeenTime:          time.Time{}, // Zero time (cleared by restore)
			expectedStatus:        domain.DeviceSummaryStatusAwaitingReconnect,
			expectedInfo:          DeviceStatusInfoAwaitingReconnect,
		},
		{
			name:                  "Post-restore with resource errors: annotation should still take precedence",
			hasAwaitingAnnotation: true,
			lastSeenTime:          time.Time{}, // Zero time
			hasResourceErrors:     true,
			expectedStatus:        domain.DeviceSummaryStatusAwaitingReconnect, // Should override resource errors
			expectedInfo:          DeviceStatusInfoAwaitingReconnect,
		},
		{
			name:                    "Post-restore with resource degradations: annotation should still take precedence",
			hasAwaitingAnnotation:   true,
			lastSeenTime:            time.Time{}, // Zero time
			hasResourceDegradations: true,
			expectedStatus:          domain.DeviceSummaryStatusAwaitingReconnect, // Should override resource degradations
			expectedInfo:            DeviceStatusInfoAwaitingReconnect,
		},
		{
			name:                  "Post-restore with rebooting: annotation should still take precedence",
			hasAwaitingAnnotation: true,
			lastSeenTime:          time.Time{}, // Zero time
			isRebooting:           true,
			expectedStatus:        domain.DeviceSummaryStatusAwaitingReconnect, // Should override rebooting
			expectedInfo:          DeviceStatusInfoAwaitingReconnect,
		},
		{
			name:                  "Without awaiting annotation: should be disconnected due to zero lastSeen",
			hasAwaitingAnnotation: false,
			lastSeenTime:          time.Time{},                       // Zero time
			expectedStatus:        domain.DeviceSummaryStatusUnknown, // Should be disconnected
		},
		{
			name:                  "With awaiting annotation but recent lastSeen: should still be AwaitingReconnect",
			hasAwaitingAnnotation: true,
			lastSeenTime:          time.Now(), // Recent time
			expectedStatus:        domain.DeviceSummaryStatusAwaitingReconnect,
			expectedInfo:          DeviceStatusInfoAwaitingReconnect,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup device with post-restore state
			annotations := make(map[string]string)
			if tt.hasAwaitingAnnotation {
				annotations[domain.DeviceAnnotationAwaitingReconnect] = "true"
			}

			device := &domain.Device{
				Metadata: domain.ObjectMeta{
					Name:        lo.ToPtr("test-device"),
					Annotations: &annotations,
				},
				Status: &domain.DeviceStatus{
					LastSeen: func() *time.Time {
						if tt.lastSeenTime.IsZero() {
							return nil
						}
						return lo.ToPtr(tt.lastSeenTime)
					}(),
					Summary: domain.DeviceSummaryStatus{
						Status: domain.DeviceSummaryStatusOnline, // Initial status (will be overridden)
						Info:   lo.ToPtr("Initial info"),
					},
					Resources: domain.DeviceResourceStatus{
						Cpu:    domain.DeviceResourceStatusHealthy,
						Memory: domain.DeviceResourceStatusHealthy,
						Disk:   domain.DeviceResourceStatusHealthy,
					},
					Conditions: []domain.Condition{},
				},
			}

			// Set up resource errors/degradations if needed
			if tt.hasResourceErrors {
				device.Status.Resources.Cpu = domain.DeviceResourceStatusCritical
			}
			if tt.hasResourceDegradations {
				device.Status.Resources.Memory = domain.DeviceResourceStatusWarning
			}

			// Set up rebooting condition if needed
			if tt.isRebooting {
				rebootCondition := domain.Condition{
					Type:   domain.ConditionTypeDeviceUpdating,
					Status: domain.ConditionStatusTrue,
					Reason: string(domain.UpdateStateRebooting),
				}
				domain.SetStatusCondition(&device.Status.Conditions, rebootCondition)
			}

			// Call the function under test
			initialStatus := device.Status.Summary.Status
			changed := updateServerSideDeviceStatus(device)

			// Verify the status was set correctly
			assert.Equal(t, tt.expectedStatus, device.Status.Summary.Status, "Status should match expected")

			if tt.expectedInfo != "" {
				assert.NotNil(t, device.Status.Summary.Info, "Info should not be nil")
				assert.Equal(t, tt.expectedInfo, *device.Status.Summary.Info, "Info should match expected")
			}

			// Verify changed flag is correct
			expectedChanged := initialStatus != tt.expectedStatus
			assert.Equal(t, expectedChanged, changed, "Changed flag should be correct")
		})
	}
}

func TestUpdateServerSideApplicationStatus_PreservesDeviceStatus(t *testing.T) {
	tests := []struct {
		name                string
		deviceSummaryStatus domain.ApplicationsSummaryStatusType
		deviceSummaryInfo   string
		appStatus           domain.ApplicationStatusType
		expectedStatus      domain.ApplicationsSummaryStatusType
		expectedInfo        string
	}{
		{
			name:                "Preserves Degraded status from device",
			deviceSummaryStatus: domain.ApplicationsSummaryStatusDegraded,
			deviceSummaryInfo:   "app1 is in status Degraded",
			appStatus:           domain.ApplicationStatusRunning,
			expectedStatus:      domain.ApplicationsSummaryStatusDegraded,
			expectedInfo:        "app1 is in status Degraded",
		},
		{
			name:                "Preserves Error status from device",
			deviceSummaryStatus: domain.ApplicationsSummaryStatusError,
			deviceSummaryInfo:   "app1 is in status Error",
			appStatus:           domain.ApplicationStatusError,
			expectedStatus:      domain.ApplicationsSummaryStatusError,
			expectedInfo:        "app1 is in status Error",
		},
		{
			name:                "Preserves Healthy status from device",
			deviceSummaryStatus: domain.ApplicationsSummaryStatusHealthy,
			deviceSummaryInfo:   "",
			appStatus:           domain.ApplicationStatusRunning,
			expectedStatus:      domain.ApplicationsSummaryStatusHealthy,
			expectedInfo:        "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			device := &domain.Device{
				Metadata: domain.ObjectMeta{
					Name: lo.ToPtr("test-device"),
				},
				Status: &domain.DeviceStatus{
					LastSeen: lo.ToPtr(time.Now()),
					ApplicationsSummary: domain.DeviceApplicationsSummaryStatus{
						Status: tt.deviceSummaryStatus,
						Info:   lo.ToPtr(tt.deviceSummaryInfo),
					},
					Applications: []domain.DeviceApplicationStatus{
						{Name: "app1", Status: tt.appStatus},
					},
				},
			}

			updateServerSideApplicationStatus(device)

			assert.Equal(t, tt.expectedStatus, device.Status.ApplicationsSummary.Status, "Status should be preserved from device")
			if tt.expectedInfo != "" {
				assert.NotNil(t, device.Status.ApplicationsSummary.Info)
				assert.Equal(t, tt.expectedInfo, *device.Status.ApplicationsSummary.Info, "Info should be preserved from device")
			}
		})
	}
}

func TestUpdateServerSideDeviceUpdatedStatus_OsImageMismatch(t *testing.T) {
	ctx := context.Background()
	orgId := uuid.New()
	log := logrus.NewEntry(logrus.StandardLogger())

	tests := []struct {
		name               string
		specOsImage        string
		specCatalogItemRef *domain.CatalogItemRefSpec
		statusOsImage      string
		capabilities       *domain.DeviceCapabilities
		expectedStatus     domain.DeviceUpdatedStatusType
		expectInfoContains string
	}{
		{
			name:           "When image-mode device has matching OS images it should remain UpToDate",
			specOsImage:    "quay.io/flightctl/device:v7",
			statusOsImage:  "quay.io/flightctl/device:v7",
			capabilities:   &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModeImage)},
			expectedStatus: domain.DeviceUpdatedStatusUpToDate,
		},
		{
			name:               "When image-mode device has mismatching OS images it should override to OutOfDate",
			specOsImage:        "quay.io/flightctl/device:v7",
			statusOsImage:      "quay.io/flightctl/device:base",
			capabilities:       &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModeImage)},
			expectedStatus:     domain.DeviceUpdatedStatusOutOfDate,
			expectInfoContains: "OS image mismatch",
		},
		{
			name:               "When package-mode device has spec OS image it should override to OutOfDate",
			specOsImage:        "quay.io/flightctl/device:v7",
			statusOsImage:      "",
			capabilities:       &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModePackage)},
			expectedStatus:     domain.DeviceUpdatedStatusOutOfDate,
			expectInfoContains: "OS image mismatch",
		},
		{
			name:           "When package-mode device has no spec OS image it should remain UpToDate",
			specOsImage:    "",
			statusOsImage:  "",
			capabilities:   &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModePackage)},
			expectedStatus: domain.DeviceUpdatedStatusUpToDate,
		},
		{
			name:           "When legacy device without capabilities has empty status OS image it should remain UpToDate",
			specOsImage:    "quay.io/flightctl/device:v7",
			statusOsImage:  "",
			capabilities:   nil,
			expectedStatus: domain.DeviceUpdatedStatusUpToDate,
		},
		{
			name:           "When legacy device without capabilities has mismatching OS images it should remain UpToDate",
			specOsImage:    "quay.io/flightctl/device:v7",
			statusOsImage:  "quay.io/flightctl/device:base",
			capabilities:   nil,
			expectedStatus: domain.DeviceUpdatedStatusUpToDate,
		},
		{
			name:           "When device has capabilities with nil osMode it should remain UpToDate",
			specOsImage:    "quay.io/flightctl/device:v7",
			statusOsImage:  "quay.io/flightctl/device:base",
			capabilities:   &domain.DeviceCapabilities{OsMode: nil},
			expectedStatus: domain.DeviceUpdatedStatusUpToDate,
		},
		{
			name:           "When no spec OS image is set it should remain UpToDate regardless of capabilities",
			specOsImage:    "",
			statusOsImage:  "quay.io/flightctl/device:base",
			capabilities:   &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModeImage)},
			expectedStatus: domain.DeviceUpdatedStatusUpToDate,
		},
		{
			name:               "When package-mode device has catalogItemRef only it should override to OutOfDate",
			specCatalogItemRef: &domain.CatalogItemRefSpec{Catalog: "cat", Item: "os", Version: "v1"},
			capabilities:       &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModePackage)},
			expectedStatus:     domain.DeviceUpdatedStatusOutOfDate,
			expectInfoContains: "catalog OS target",
		},
		{
			name:               "When image-mode device has catalogItemRef only it should remain UpToDate",
			specCatalogItemRef: &domain.CatalogItemRefSpec{Catalog: "cat", Item: "os", Version: "v1"},
			capabilities:       &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModeImage)},
			expectedStatus:     domain.DeviceUpdatedStatusUpToDate,
		},
		{
			name:               "When legacy device without capabilities has catalogItemRef it should remain UpToDate",
			specCatalogItemRef: &domain.CatalogItemRefSpec{Catalog: "cat", Item: "os", Version: "v1"},
			capabilities:       nil,
			expectedStatus:     domain.DeviceUpdatedStatusUpToDate,
		},
		{
			name:               "When device has capabilities with nil osMode and catalogItemRef it should remain UpToDate",
			specCatalogItemRef: &domain.CatalogItemRefSpec{Catalog: "cat", Item: "os", Version: "v1"},
			capabilities:       &domain.DeviceCapabilities{OsMode: nil},
			expectedStatus:     domain.DeviceUpdatedStatusUpToDate,
		},
		{
			name:           "When package-mode device has no OS target it should remain UpToDate",
			capabilities:   &domain.DeviceCapabilities{OsMode: lo.ToPtr(domain.OsModePackage)},
			expectedStatus: domain.DeviceUpdatedStatusUpToDate,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			annotations := map[string]string{
				domain.DeviceAnnotationRenderedVersion: "4",
			}
			device := &domain.Device{
				Metadata: domain.ObjectMeta{
					Name:        lo.ToPtr("test-device"),
					Annotations: &annotations,
				},
				Spec: &domain.DeviceSpec{},
				Status: &domain.DeviceStatus{
					LastSeen: lo.ToPtr(time.Now()),
					Updated: domain.DeviceUpdatedStatus{
						Status: domain.DeviceUpdatedStatusUpToDate,
					},
					Config: domain.DeviceConfigStatus{
						RenderedVersion: "4",
					},
					Os: domain.DeviceOsStatus{
						Image: tt.statusOsImage,
					},
				},
			}
			if tt.specOsImage != "" || tt.specCatalogItemRef != nil {
				device.Spec.Os = &domain.DeviceOsSpec{
					Image:          tt.specOsImage,
					CatalogItemRef: tt.specCatalogItemRef,
				}
			}
			device.Status.Capabilities = tt.capabilities

			updateServerSideDeviceUpdatedStatus(device, ctx, nil, log, orgId)

			assert.Equal(t, tt.expectedStatus, device.Status.Updated.Status)
			if tt.expectInfoContains != "" {
				assert.Contains(t, *device.Status.Updated.Info, tt.expectInfoContains)
			}
		})
	}
}

func TestUpdateServerSideLifecycleStatus(t *testing.T) {
	tests := []struct {
		name           string
		currentStatus  domain.DeviceLifecycleStatusType
		currentInfo    string
		condition      *domain.Condition
		expectedStatus domain.DeviceLifecycleStatusType
		expectedInfo   string
		expectedChange bool
	}{
		{
			name:           "When no decommissioning condition exists it should not change status",
			currentStatus:  domain.DeviceLifecycleStatusEnrolled,
			condition:      nil,
			expectedStatus: domain.DeviceLifecycleStatusEnrolled,
			expectedChange: false,
		},
		{
			name:          "When device is Enrolled and agent reports DecomStarted it should transition to Decommissioning",
			currentStatus: domain.DeviceLifecycleStatusEnrolled,
			condition: &domain.Condition{
				Type:   domain.ConditionTypeDeviceDecommissioning,
				Status: domain.ConditionStatusTrue,
				Reason: string(domain.DecommissionStateStarted),
			},
			expectedStatus: domain.DeviceLifecycleStatusDecommissioning,
			expectedInfo:   "Device has acknowledged decommissioning request",
			expectedChange: true,
		},
		{
			name:          "When device is Decommissioning and agent reports DecomComplete it should transition to Decommissioned",
			currentStatus: domain.DeviceLifecycleStatusDecommissioning,
			currentInfo:   "Device has acknowledged decommissioning request",
			condition: &domain.Condition{
				Type:   domain.ConditionTypeDeviceDecommissioning,
				Status: domain.ConditionStatusTrue,
				Reason: string(domain.DecommissionStateComplete),
			},
			expectedStatus: domain.DeviceLifecycleStatusDecommissioned,
			expectedInfo:   "Device has completed decommissioning",
			expectedChange: true,
		},
		{
			name:          "When device is Decommissioning and agent reports DecomError it should transition to Decommissioned",
			currentStatus: domain.DeviceLifecycleStatusDecommissioning,
			currentInfo:   "Device has acknowledged decommissioning request",
			condition: &domain.Condition{
				Type:   domain.ConditionTypeDeviceDecommissioning,
				Status: domain.ConditionStatusTrue,
				Reason: string(domain.DecommissionStateError),
			},
			expectedStatus: domain.DeviceLifecycleStatusDecommissioned,
			expectedInfo:   "Device has errored while decommissioning",
			expectedChange: true,
		},
		{
			name:          "When device is Decommissioned and agent reports DecomStarted it should not transition back to Decommissioning",
			currentStatus: domain.DeviceLifecycleStatusDecommissioned,
			currentInfo:   "Device has completed decommissioning",
			condition: &domain.Condition{
				Type:   domain.ConditionTypeDeviceDecommissioning,
				Status: domain.ConditionStatusTrue,
				Reason: string(domain.DecommissionStateStarted),
			},
			expectedStatus: domain.DeviceLifecycleStatusDecommissioned,
			expectedInfo:   "Device has completed decommissioning",
			expectedChange: false,
		},
		{
			name:          "When device is Decommissioned and agent reports DecomComplete it should remain Decommissioned",
			currentStatus: domain.DeviceLifecycleStatusDecommissioned,
			currentInfo:   "Device has errored while decommissioning",
			condition: &domain.Condition{
				Type:   domain.ConditionTypeDeviceDecommissioning,
				Status: domain.ConditionStatusTrue,
				Reason: string(domain.DecommissionStateComplete),
			},
			expectedStatus: domain.DeviceLifecycleStatusDecommissioned,
			expectedInfo:   "Device has errored while decommissioning",
			expectedChange: false,
		},
		{
			name:          "When device is Decommissioned and agent reports DecomError it should remain Decommissioned",
			currentStatus: domain.DeviceLifecycleStatusDecommissioned,
			currentInfo:   "Device has completed decommissioning",
			condition: &domain.Condition{
				Type:   domain.ConditionTypeDeviceDecommissioning,
				Status: domain.ConditionStatusTrue,
				Reason: string(domain.DecommissionStateError),
			},
			expectedStatus: domain.DeviceLifecycleStatusDecommissioned,
			expectedInfo:   "Device has completed decommissioning",
			expectedChange: false,
		},
		{
			name:          "When device is Decommissioning and agent resends DecomStarted it should not change",
			currentStatus: domain.DeviceLifecycleStatusDecommissioning,
			currentInfo:   "Device has acknowledged decommissioning request",
			condition: &domain.Condition{
				Type:   domain.ConditionTypeDeviceDecommissioning,
				Status: domain.ConditionStatusTrue,
				Reason: string(domain.DecommissionStateStarted),
			},
			expectedStatus: domain.DeviceLifecycleStatusDecommissioning,
			expectedInfo:   "Device has acknowledged decommissioning request",
			expectedChange: false,
		},
		{
			name:          "When device is Unknown and agent reports DecomStarted it should transition to Decommissioning",
			currentStatus: domain.DeviceLifecycleStatusUnknown,
			condition: &domain.Condition{
				Type:   domain.ConditionTypeDeviceDecommissioning,
				Status: domain.ConditionStatusTrue,
				Reason: string(domain.DecommissionStateStarted),
			},
			expectedStatus: domain.DeviceLifecycleStatusDecommissioning,
			expectedInfo:   "Device has acknowledged decommissioning request",
			expectedChange: true,
		},
		{
			name:          "When device is Enrolled and agent reports DecomComplete it should transition to Decommissioned",
			currentStatus: domain.DeviceLifecycleStatusEnrolled,
			condition: &domain.Condition{
				Type:   domain.ConditionTypeDeviceDecommissioning,
				Status: domain.ConditionStatusTrue,
				Reason: string(domain.DecommissionStateComplete),
			},
			expectedStatus: domain.DeviceLifecycleStatusDecommissioned,
			expectedInfo:   "Device has completed decommissioning",
			expectedChange: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conditions := []domain.Condition{}
			if tt.condition != nil {
				conditions = append(conditions, *tt.condition)
			}

			device := &domain.Device{
				Metadata: domain.ObjectMeta{
					Name: lo.ToPtr("test-device"),
				},
				Status: &domain.DeviceStatus{
					Lifecycle: domain.DeviceLifecycleStatus{
						Status: tt.currentStatus,
						Info: func() *string {
							if tt.currentInfo != "" {
								return lo.ToPtr(tt.currentInfo)
							}
							return nil
						}(),
					},
					Conditions: conditions,
				},
			}

			changed := updateServerSideLifecycleStatus(device)

			assert.Equal(t, tt.expectedStatus, device.Status.Lifecycle.Status, "lifecycle status")
			if tt.expectedInfo != "" {
				assert.NotNil(t, device.Status.Lifecycle.Info)
				assert.Equal(t, tt.expectedInfo, *device.Status.Lifecycle.Info, "lifecycle info")
			}
			assert.Equal(t, tt.expectedChange, changed, "changed flag")
		})
	}
}

func TestUpdateServerSideDeviceUpdatedStatus_ManagedDeviceErrorPriority(t *testing.T) {
	ctx := context.Background()
	orgId := uuid.New()
	log := logrus.NewEntry(logrus.StandardLogger())

	fleetName := "test-fleet"
	fleetTV := "v2"
	fleet := &domain.Fleet{
		Metadata: domain.ObjectMeta{
			Name:        &fleetName,
			Annotations: &map[string]string{domain.FleetAnnotationTemplateVersion: fleetTV},
		},
	}
	fs := &stubFleetStore{fleet: fleet}
	owner := "Fleet/test-fleet"

	tests := []struct {
		name            string
		annotations     map[string]string
		conditions      []domain.Condition
		expectedContain string
	}{
		{
			name: "When only lastRolloutError is present it should show the rollout error",
			annotations: map[string]string{
				domain.DeviceAnnotationTemplateVersion:  "v1",
				domain.DeviceAnnotationLastRolloutError: "failed replacing parameters in env var DNS_SERVER_DOMAIN",
			},
			conditions:      nil,
			expectedContain: "failed replacing parameters in env var DNS_SERVER_DOMAIN",
		},
		{
			name: "When both lastRolloutError and DeviceUpdating error exist it should prefer rollout error",
			annotations: map[string]string{
				domain.DeviceAnnotationTemplateVersion:  "v1",
				domain.DeviceAnnotationLastRolloutError: "template rendering failure",
			},
			conditions: []domain.Condition{
				{
					Type:    domain.ConditionTypeDeviceUpdating,
					Status:  domain.ConditionStatusFalse,
					Reason:  string(domain.UpdateStateError),
					Message: "old agent update error",
				},
			},
			expectedContain: "template rendering failure",
		},
		{
			name: "When only DeviceUpdating error is present it should show the agent error",
			annotations: map[string]string{
				domain.DeviceAnnotationTemplateVersion: "v1",
			},
			conditions: []domain.Condition{
				{
					Type:    domain.ConditionTypeDeviceUpdating,
					Status:  domain.ConditionStatusFalse,
					Reason:  string(domain.UpdateStateError),
					Message: "agent update failed",
				},
			},
			expectedContain: "agent update failed",
		},
		{
			name: "When neither error source is present it should show generic out-of-sync message",
			annotations: map[string]string{
				domain.DeviceAnnotationTemplateVersion: "v1",
			},
			conditions:      nil,
			expectedContain: domain.DeviceOutOfSyncWithFleetText,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			device := &domain.Device{
				Metadata: domain.ObjectMeta{
					Name:        lo.ToPtr("test-device"),
					Owner:       &owner,
					Annotations: &tt.annotations,
				},
				Spec: &domain.DeviceSpec{},
				Status: &domain.DeviceStatus{
					LastSeen:   lo.ToPtr(time.Now()),
					Updated:    domain.DeviceUpdatedStatus{Status: domain.DeviceUpdatedStatusOutOfDate},
					Conditions: tt.conditions,
				},
			}

			changed := updateServerSideDeviceUpdatedStatus(device, ctx, fs, log, orgId)

			assert.False(t, changed, "status enum did not change so changed must be false")
			assert.Equal(t, domain.DeviceUpdatedStatusOutOfDate, device.Status.Updated.Status)
			assert.Contains(t, *device.Status.Updated.Info, tt.expectedContain)
		})
	}
}
