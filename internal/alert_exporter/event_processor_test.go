package alert_exporter

import (
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/google/uuid"
	"github.com/samber/lo"
)

func fakeEvent(org, kind, name, reason string) api.Event {
	return api.Event{
		Metadata: api.ObjectMeta{
			CreationTimestamp: lo.ToPtr(time.Now()),
		},
		Reason: api.EventReason(reason),
		InvolvedObject: api.ObjectReference{
			Kind: kind,
			Name: name,
		},
	}
}

func TestSetExclusiveAlert(t *testing.T) {
	testOrgID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	now := time.Now()
	event1 := fakeEvent("org", "Device", "dev1", string(api.EventReasonDeviceCPUCritical))
	event2 := fakeEvent("org", "Device", "dev1", string(api.EventReasonDeviceDiskWarning))
	alerts := map[AlertKey]map[string]*AlertInfo{
		AlertKeyFromEvent(event1, testOrgID): {
			string(api.EventReasonDeviceCPUWarning):   &AlertInfo{StartsAt: now},
			string(api.EventReasonDeviceDiskCritical): &AlertInfo{StartsAt: now},
			string(api.EventReasonDeviceDisconnected): &AlertInfo{StartsAt: now}, // should remain
		},
	}
	checkpointCtx := &CheckpointContext{
		alerts: alerts,
	}

	checkpointCtx.setAlert(event1, string(api.EventReasonDeviceCPUCritical), cpuGroup, testOrgID)
	checkpointCtx.setAlert(event2, string(api.EventReasonDeviceDiskWarning), diskGroup, testOrgID)

	reasons := alerts[AlertKeyFromEvent(event1, testOrgID)]
	if reasons[string(api.EventReasonDeviceCPUWarning)] == nil || reasons[string(api.EventReasonDeviceCPUWarning)].EndsAt == nil {
		t.Errorf("expected DeviceCPUWarning to be resolved")
	}
	if reasons[string(api.EventReasonDeviceCPUCritical)] == nil || reasons[string(api.EventReasonDeviceCPUCritical)].EndsAt != nil {
		t.Errorf("expected DeviceCPUCritical to be active")
	}
	if reasons[string(api.EventReasonDeviceDiskCritical)] == nil || reasons[string(api.EventReasonDeviceDiskCritical)].EndsAt == nil {
		t.Errorf("expected DeviceDiskCritical to be resolved")
	}
	if reasons[string(api.EventReasonDeviceDiskWarning)] == nil || reasons[string(api.EventReasonDeviceDiskWarning)].EndsAt != nil {
		t.Errorf("expected DeviceDiskWarning to be active")
	}
	if reasons[string(api.EventReasonDeviceDisconnected)] == nil || reasons[string(api.EventReasonDeviceDisconnected)].EndsAt != nil {
		t.Errorf("expected DeviceDisconnected to be active")
	}
}

func TestClearAlertGroup(t *testing.T) {
	testOrgID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	now := time.Now()
	event := fakeEvent("org", "Device", "dev1", string(api.EventReasonDeviceMemoryNormal))
	key := AlertKeyFromEvent(event, testOrgID)
	alerts := map[AlertKey]map[string]*AlertInfo{
		key: {
			string(api.EventReasonDeviceMemoryWarning):  {StartsAt: now},
			string(api.EventReasonDeviceMemoryCritical): {StartsAt: now},
		},
	}
	checkpointCtx := &CheckpointContext{
		alerts: alerts,
	}
	checkpointCtx.clearAlertGroup(event, memoryGroup, testOrgID)

	if alerts[key][string(api.EventReasonDeviceMemoryWarning)].EndsAt == nil {
		t.Errorf("expected DeviceMemoryWarning to be resolved")
	}
	if alerts[key][string(api.EventReasonDeviceMemoryCritical)].EndsAt == nil {
		t.Errorf("expected DeviceMemoryCritical to be resolved")
	}
}

func TestProcessEvent_AppStatus(t *testing.T) {
	testOrgID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	event := fakeEvent("org", "Device", "dev1", string(api.EventReasonDeviceApplicationError))
	key := AlertKeyFromEvent(event, testOrgID)
	checkpointCtx := &CheckpointContext{
		alerts: make(map[AlertKey]map[string]*AlertInfo),
	}

	checkpointCtx.processEvent(event, testOrgID)

	reasons := checkpointCtx.alerts[key]
	if reasons[string(api.EventReasonDeviceApplicationError)].EndsAt != nil || reasons[string(api.EventReasonDeviceApplicationError)].StartsAt != *event.Metadata.CreationTimestamp {
		t.Errorf("expected DeviceApplicationError to be set")
	}
}

func TestProcessEvent_AppHealthy(t *testing.T) {
	testOrgID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	event := fakeEvent("org", "Device", "dev1", string(api.EventReasonDeviceApplicationHealthy))
	key := AlertKeyFromEvent(event, testOrgID)
	alerts := map[AlertKey]map[string]*AlertInfo{
		key: {
			string(api.EventReasonDeviceApplicationError):    {StartsAt: time.Now()},
			string(api.EventReasonDeviceApplicationDegraded): {StartsAt: time.Now()},
		},
	}

	checkpointCtx := &CheckpointContext{
		alerts: alerts,
	}
	checkpointCtx.processEvent(event, testOrgID)

	if alerts[key][string(api.EventReasonDeviceApplicationError)].EndsAt == nil {
		t.Errorf("expected DeviceApplicationError to be resolved")
	}
	if alerts[key][string(api.EventReasonDeviceApplicationDegraded)].EndsAt == nil {
		t.Errorf("expected DeviceApplicationDegraded to be resolved")
	}
}

func TestProcessEvent_Connected(t *testing.T) {
	testOrgID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	event := fakeEvent("org", "Device", "dev1", string(api.EventReasonDeviceConnected))
	key := AlertKeyFromEvent(event, testOrgID)
	alerts := map[AlertKey]map[string]*AlertInfo{
		key: {string(api.EventReasonDeviceDisconnected): {StartsAt: time.Now()}},
	}

	checkpointCtx := &CheckpointContext{
		alerts: alerts,
	}
	checkpointCtx.processEvent(event, testOrgID)

	if alerts[key][string(api.EventReasonDeviceDisconnected)].EndsAt == nil {
		t.Errorf("expected DeviceDisconnected to be resolved")
	}
}

func TestProcessEvent_ResourceDeleted(t *testing.T) {
	testOrgID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	event := fakeEvent("org", "Device", "dev1", string(api.EventReasonResourceDeleted))
	key := AlertKeyFromEvent(event, testOrgID)

	alerts := map[AlertKey]map[string]*AlertInfo{
		key: {
			string(api.EventReasonDeviceMemoryWarning): {StartsAt: time.Now()},
			string(api.EventReasonDeviceDiskCritical):  {StartsAt: time.Now()},
		},
	}

	checkpointCtx := &CheckpointContext{
		alerts: alerts,
	}
	checkpointCtx.processEvent(event, testOrgID)

	if alerts[key][string(api.EventReasonDeviceMemoryWarning)].EndsAt == nil {
		t.Errorf("expected DeviceMemoryWarning to be resolved")
	}
	if alerts[key][string(api.EventReasonDeviceDiskCritical)].EndsAt == nil {
		t.Errorf("expected DeviceDiskCritical to be resolved")
	}
}

func TestEventToAlertConversion(t *testing.T) {
	now := time.Now()
	checkpoint := &AlertCheckpoint{
		Version:   CurrentAlertCheckpointVersion,
		Alerts:    make(map[AlertKey]map[string]*AlertInfo),
		Timestamp: now.Format(time.RFC3339Nano),
	}

	testCases := []struct {
		name   string
		events []api.Event
		checks func(t *testing.T, checkpoint *AlertCheckpoint)
	}{
		{
			name: "device disconnected",
			events: []api.Event{
				fakeEvent("myorg", "Device", "device1", "DeviceDisconnected"),
			},
			checks: func(t *testing.T, checkpoint *AlertCheckpoint) {
				k := NewAlertKey("00000000-0000-0000-0000-000000000000", "Device", "device1")
				if _, exists := checkpoint.Alerts[k]; !exists {
					t.Errorf("Expected alert for device1 but not found")
				}
				if alert, exists := checkpoint.Alerts[k]["DeviceDisconnected"]; !exists || alert.EndsAt != nil {
					t.Errorf("Expected active DeviceDisconnected alert")
				}
			},
		},
		{
			name: "device connected after disconnected",
			events: []api.Event{
				fakeEvent("myorg", "Device", "device1", "DeviceDisconnected"),
				fakeEvent("myorg", "Device", "device1", "DeviceConnected"),
			},
			checks: func(t *testing.T, checkpoint *AlertCheckpoint) {
				k := NewAlertKey("00000000-0000-0000-0000-000000000000", "Device", "device1")
				if alert, exists := checkpoint.Alerts[k]["DeviceDisconnected"]; !exists || alert.EndsAt == nil {
					t.Errorf("Expected resolved DeviceDisconnected alert")
				}
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			checkpointCtx := CheckpointContext{
				alerts: make(map[AlertKey]map[string]*AlertInfo),
			}

			testOrgID := uuid.MustParse("00000000-0000-0000-0000-000000000000")
			for _, event := range tc.events {
				checkpointCtx.processEvent(event, testOrgID)
			}

			checkpoint.Alerts = checkpointCtx.alerts
			tc.checks(t, checkpoint)
		})
	}
}

func TestMultiOrgEventProcessing(t *testing.T) {
	org1ID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	org2ID := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	event1 := fakeEvent("org1", "Device", "device1", string(api.EventReasonDeviceCPUCritical))
	event2 := fakeEvent("org2", "Device", "device1", string(api.EventReasonDeviceCPUCritical))

	checkpointCtx := &CheckpointContext{
		alerts: make(map[AlertKey]map[string]*AlertInfo),
	}

	checkpointCtx.processEvent(event1, org1ID)
	checkpointCtx.processEvent(event2, org2ID)

	// Verify alerts are created for both orgs with same device name
	key1 := AlertKeyFromEvent(event1, org1ID)
	key2 := AlertKeyFromEvent(event2, org2ID)

	if key1 == key2 {
		t.Errorf("Expected different alert keys for different orgs, got same key: %s", key1)
	}

	// Verify org1 alert
	if alert1, exists := checkpointCtx.alerts[key1][string(api.EventReasonDeviceCPUCritical)]; !exists {
		t.Errorf("Expected alert for org1")
	} else {
		if alert1.OrgID != org1ID.String() {
			t.Errorf("Expected org1 ID %s, got %s", org1ID.String(), alert1.OrgID)
		}
		if alert1.ResourceName != "device1" {
			t.Errorf("Expected device1, got %s", alert1.ResourceName)
		}
	}

	// Verify org2 alert
	if alert2, exists := checkpointCtx.alerts[key2][string(api.EventReasonDeviceCPUCritical)]; !exists {
		t.Errorf("Expected alert for org2")
	} else {
		if alert2.OrgID != org2ID.String() {
			t.Errorf("Expected org2 ID %s, got %s", org2ID.String(), alert2.OrgID)
		}
		if alert2.ResourceName != "device1" {
			t.Errorf("Expected device1, got %s", alert2.ResourceName)
		}
	}
}

func TestOrgIsolation(t *testing.T) {
	org1ID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	org2ID := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	event1 := fakeEvent("org1", "Device", "device1", string(api.EventReasonDeviceCPUCritical))
	event2 := fakeEvent("org2", "Device", "device1", string(api.EventReasonDeviceCPUNormal))

	checkpointCtx := &CheckpointContext{
		alerts: make(map[AlertKey]map[string]*AlertInfo),
	}

	// Process events
	checkpointCtx.processEvent(event1, org1ID)
	checkpointCtx.processEvent(event2, org2ID)

	// Verify org1 still has active CPU critical alert
	key1 := AlertKeyFromEvent(event1, org1ID)
	if alert, exists := checkpointCtx.alerts[key1][string(api.EventReasonDeviceCPUCritical)]; !exists || alert.EndsAt != nil {
		t.Errorf("Expected active CPU critical alert for org1")
	}

	// Verify org2 has no CPU alerts (CPU normal should not create alerts, only clear them)
	key2 := AlertKeyFromEvent(event2, org2ID)
	if _, exists := checkpointCtx.alerts[key2]; exists {
		if len(checkpointCtx.alerts[key2]) > 0 {
			t.Errorf("Expected no alerts for org2, got %d", len(checkpointCtx.alerts[key2]))
		}
	}
}
