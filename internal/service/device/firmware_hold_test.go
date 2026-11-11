package device

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var holdOutstandingPhases = []domain.FirmwareAttemptPhase{
	domain.FirmwareAttemptPhasePreparing,
	domain.FirmwareAttemptPhaseWaitingForPolicy,
	domain.FirmwareAttemptPhaseStaging,
	domain.FirmwareAttemptPhaseAwaitingReboot,
	domain.FirmwareAttemptPhaseVerifying,
	"Mystery",
	"",
}

var holdTerminalPhases = []domain.FirmwareAttemptPhase{
	domain.FirmwareAttemptPhaseSucceeded,
	domain.FirmwareAttemptPhaseFailed,
	domain.FirmwareAttemptPhaseCanceled,
}

func holdTestFirmware() *domain.FirmwareSpec {
	var firmware domain.FirmwareSpec
	if err := json.Unmarshal([]byte(`{"profile":"board-a","updater":{"name":"vendor-updater","version":"1.0.0","config":{}},`+
		`"components":[{"name":"bios","version":{"exact":"2.0"}}],"overlays":[]}`), &firmware); err != nil {
		panic(err)
	}
	return &firmware
}

func holdTestAttempt(phase domain.FirmwareAttemptPhase) *domain.FirmwareStatus {
	return &domain.FirmwareStatus{
		Support:    domain.FirmwareSupportStatus{ApiVersion: 1},
		Components: []domain.FirmwareComponentStatus{},
		Attempt:    &domain.FirmwareAttemptStatus{Id: "attempt-1", Phase: phase, Recovery: []domain.FirmwareRecoveryAction{}},
	}
}

// holdTestDevice creates a device that was rendered at version 1 with spec
// hash "hash" and has a valid spec.
func holdTestDevice(t *testing.T, st *fakeStore, orgId uuid.UUID, owner *string, firmware *domain.FirmwareSpec, reported *domain.FirmwareStatus) {
	t.Helper()
	status := domain.NewDeviceStatus()
	status.Firmware = reported
	status.Conditions = []domain.Condition{{Type: domain.ConditionTypeDeviceSpecValid, Status: domain.ConditionStatusTrue, Reason: "Valid"}}
	_, err := st.device.Create(context.Background(), orgId, &domain.Device{
		Metadata: domain.ObjectMeta{
			Name:  lo.ToPtr("dev"),
			Owner: owner,
			Annotations: &map[string]string{
				domain.DeviceAnnotationRenderedSpecHash: "hash",
				domain.DeviceAnnotationRenderedVersion:  "1",
			},
		},
		Spec:   &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "os:1"}, Firmware: firmware},
		Status: &status,
	}, nil)
	require.NoError(t, err)
}

func requireFirmwareHoldCondition(t *testing.T, device *domain.Device) {
	t.Helper()
	condition := domain.FindStatusCondition(device.Status.Conditions, domain.ConditionTypeDeviceSpecValid)
	require.NotNil(t, condition)
	assert.Equal(t, domain.ConditionStatusUnknown, condition.Status)
	assert.Equal(t, domain.DeviceSpecValidReasonFirmwareUnsupported, condition.Reason)
}

func TestUpdateRenderedDeviceFirmwareHold(t *testing.T) {
	requireHeld := func(t *testing.T, st *fakeStore, ev *fakeEvents, status domain.Status, before *domain.Device) {
		t.Helper()
		require.True(t, IsFirmwareHold(status), status.Message)
		after := st.device.devices["dev"]
		assert.Nil(t, st.device.rendered["dev"], "a held device gets no rendered output")
		assert.Equal(t, before.Metadata.Annotations, after.Metadata.Annotations)
		assert.Equal(t, before.Spec, after.Spec)
		assert.Equal(t, before.Status.Firmware, after.Status.Firmware)
		requireFirmwareHoldCondition(t, after)
		for _, event := range ev.created {
			assert.NotEqual(t, domain.EventReasonResourceUpdated, event.Reason)
			assert.NotEqual(t, domain.EventReasonDeviceSpecValid, event.Reason)
		}
	}

	t.Run("When the device has desired firmware it should hold the rendered write", func(t *testing.T) {
		st, ev, svc := newTestHandler()
		orgId := uuid.New()
		holdTestDevice(t, st, orgId, nil, holdTestFirmware(), nil)
		before := deepCopyDevice(st.device.devices["dev"])

		status := svc.UpdateRenderedDevice(context.Background(), orgId, "dev", "config", "apps", "new-hash", "", nil, false, nil)

		requireHeld(t, st, ev, status, before)
	})

	for _, phase := range holdOutstandingPhases {
		t.Run("When the device has an outstanding "+string(phase)+" attempt and no desired firmware it should hold the rendered write", func(t *testing.T) {
			st, ev, svc := newTestHandler()
			orgId := uuid.New()
			holdTestDevice(t, st, orgId, nil, nil, holdTestAttempt(phase))
			before := deepCopyDevice(st.device.devices["dev"])

			status := svc.UpdateRenderedDevice(context.Background(), orgId, "dev", "config", "apps", "new-hash", "", nil, true, nil)

			requireHeld(t, st, ev, status, before)
		})
	}

	t.Run("When the hold is already recorded it should write nothing", func(t *testing.T) {
		st, _, svc := newTestHandler()
		orgId := uuid.New()
		holdTestDevice(t, st, orgId, nil, holdTestFirmware(), nil)
		require.True(t, IsFirmwareHold(svc.UpdateRenderedDevice(context.Background(), orgId, "dev", "config", "apps", "new-hash", "", nil, false, nil)))
		writes := st.device.deviceWrites

		status := svc.UpdateRenderedDevice(context.Background(), orgId, "dev", "config", "apps", "new-hash", "", nil, false, nil)

		require.True(t, IsFirmwareHold(status))
		assert.Equal(t, writes, st.device.deviceWrites)
	})

	t.Run("When firmware state appears before an unchanged render marks the spec valid it should hold instead", func(t *testing.T) {
		st, ev, svc := newTestHandler()
		orgId := uuid.New()
		holdTestDevice(t, st, orgId, nil, nil, nil)
		st.device.devices["dev"].Status.Conditions = []domain.Condition{{Type: domain.ConditionTypeDeviceSpecValid, Status: domain.ConditionStatusFalse, Reason: "Invalid"}}
		st.device.beforeMutate = func(call int) {
			if call == 2 {
				st.device.devices["dev"].Status.Firmware = holdTestAttempt(domain.FirmwareAttemptPhaseStaging)
			}
		}

		status := svc.UpdateRenderedDevice(context.Background(), orgId, "dev", "config", "apps", "hash", "", nil, false, nil)

		require.True(t, IsFirmwareHold(status), status.Message)
		requireFirmwareHoldCondition(t, st.device.devices["dev"])
		for _, event := range ev.created {
			assert.NotEqual(t, domain.EventReasonDeviceSpecValid, event.Reason)
		}
	})

	for _, phase := range holdTerminalPhases {
		t.Run("When the device has only a "+string(phase)+" attempt and an unchanged render it should mark the spec valid", func(t *testing.T) {
			st, _, svc := newTestHandler()
			orgId := uuid.New()
			holdTestDevice(t, st, orgId, nil, nil, holdTestAttempt(phase))
			st.device.devices["dev"].Status.Conditions = nil

			status := svc.UpdateRenderedDevice(context.Background(), orgId, "dev", "config", "apps", "hash", "", nil, false, nil)

			require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
			condition := domain.FindStatusCondition(st.device.devices["dev"].Status.Conditions, domain.ConditionTypeDeviceSpecValid)
			require.NotNil(t, condition)
			assert.Equal(t, domain.ConditionStatusTrue, condition.Status)
			assert.Equal(t, holdTestAttempt(phase), st.device.devices["dev"].Status.Firmware)
		})
	}
}

func TestReplaceDeviceSpecFirmwareHold(t *testing.T) {
	owner := lo.ToPtr("Fleet/fleet-a")
	newSpec := domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "os:2"}}
	setAnnotations := map[string]string{domain.DeviceAnnotationTemplateVersion: "tv-2"}

	t.Run("When the device has desired firmware it should keep its spec and record the hold", func(t *testing.T) {
		st, _, svc := newTestHandler()
		orgId := uuid.New()
		holdTestDevice(t, st, orgId, owner, holdTestFirmware(), nil)
		before := deepCopyDevice(st.device.devices["dev"])

		_, status := svc.ReplaceDeviceSpec(context.Background(), orgId, "dev", owner, newSpec, setAnnotations, nil)

		require.True(t, IsFirmwareHold(status), status.Message)
		after := st.device.devices["dev"]
		assert.Equal(t, before.Spec, after.Spec)
		assert.Equal(t, before.Metadata.Annotations, after.Metadata.Annotations)
		requireFirmwareHoldCondition(t, after)
	})

	for _, phase := range holdOutstandingPhases {
		t.Run("When the device has an outstanding "+string(phase)+" attempt it should keep its spec and attempt and record the hold", func(t *testing.T) {
			st, _, svc := newTestHandler()
			orgId := uuid.New()
			holdTestDevice(t, st, orgId, owner, nil, holdTestAttempt(phase))
			before := deepCopyDevice(st.device.devices["dev"])

			_, status := svc.ReplaceDeviceSpec(context.Background(), orgId, "dev", owner, newSpec, setAnnotations, nil)

			require.True(t, IsFirmwareHold(status), status.Message)
			after := st.device.devices["dev"]
			assert.Equal(t, before.Spec, after.Spec)
			assert.Equal(t, before.Status.Firmware, after.Status.Firmware)
			assert.NotContains(t, lo.FromPtr(after.Metadata.Annotations), domain.DeviceAnnotationTemplateVersion)
			requireFirmwareHoldCondition(t, after)
		})
	}

	for _, phase := range holdTerminalPhases {
		t.Run("When the device has only a "+string(phase)+" attempt it should replace its spec", func(t *testing.T) {
			st, _, svc := newTestHandler()
			orgId := uuid.New()
			holdTestDevice(t, st, orgId, owner, nil, holdTestAttempt(phase))

			_, status := svc.ReplaceDeviceSpec(context.Background(), orgId, "dev", owner, newSpec, setAnnotations, nil)

			require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
			after := st.device.devices["dev"]
			assert.Equal(t, newSpec, *after.Spec)
			assert.Equal(t, "tv-2", lo.FromPtr(after.Metadata.Annotations)[domain.DeviceAnnotationTemplateVersion])
		})
	}

	t.Run("When the owner changed it should report the owner conflict instead of a hold", func(t *testing.T) {
		st, _, svc := newTestHandler()
		orgId := uuid.New()
		holdTestDevice(t, st, orgId, lo.ToPtr("Fleet/other"), holdTestFirmware(), nil)
		writes := st.device.deviceWrites

		_, status := svc.ReplaceDeviceSpec(context.Background(), orgId, "dev", owner, newSpec, setAnnotations, nil)

		require.Equal(t, int32(http.StatusConflict), status.Code)
		assert.False(t, IsFirmwareHold(status))
		assert.Equal(t, writes, st.device.deviceWrites)
	})
}

func TestHoldDeviceForFirmware(t *testing.T) {
	owner := lo.ToPtr("Fleet/fleet-a")

	t.Run("When the source carries firmware it should hold a device without firmware state", func(t *testing.T) {
		st, _, svc := newTestHandler()
		orgId := uuid.New()
		holdTestDevice(t, st, orgId, owner, nil, nil)

		held, status := svc.HoldDeviceForFirmware(context.Background(), orgId, "dev", owner, true)

		require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
		assert.True(t, held)
		requireFirmwareHoldCondition(t, st.device.devices["dev"])
	})

	t.Run("When the device has desired firmware it should hold it", func(t *testing.T) {
		st, _, svc := newTestHandler()
		orgId := uuid.New()
		holdTestDevice(t, st, orgId, owner, holdTestFirmware(), nil)

		held, status := svc.HoldDeviceForFirmware(context.Background(), orgId, "dev", owner, false)

		require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
		assert.True(t, held)
		requireFirmwareHoldCondition(t, st.device.devices["dev"])
	})

	t.Run("When the device has an outstanding attempt it should hold it and keep the attempt", func(t *testing.T) {
		st, _, svc := newTestHandler()
		orgId := uuid.New()
		holdTestDevice(t, st, orgId, owner, nil, holdTestAttempt(domain.FirmwareAttemptPhaseAwaitingReboot))

		held, status := svc.HoldDeviceForFirmware(context.Background(), orgId, "dev", owner, false)

		require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
		assert.True(t, held)
		assert.Equal(t, holdTestAttempt(domain.FirmwareAttemptPhaseAwaitingReboot), st.device.devices["dev"].Status.Firmware)
		requireFirmwareHoldCondition(t, st.device.devices["dev"])
	})

	t.Run("When neither the source nor the device has firmware state it should leave the device unchanged", func(t *testing.T) {
		st, _, svc := newTestHandler()
		orgId := uuid.New()
		holdTestDevice(t, st, orgId, owner, nil, holdTestAttempt(domain.FirmwareAttemptPhaseSucceeded))
		before := deepCopyDevice(st.device.devices["dev"])
		writes := st.device.deviceWrites

		held, status := svc.HoldDeviceForFirmware(context.Background(), orgId, "dev", owner, false)

		require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
		assert.False(t, held)
		assert.Equal(t, writes, st.device.deviceWrites)
		assert.Equal(t, before, st.device.devices["dev"])
	})

	t.Run("When the hold is already recorded it should write nothing", func(t *testing.T) {
		st, _, svc := newTestHandler()
		orgId := uuid.New()
		holdTestDevice(t, st, orgId, owner, holdTestFirmware(), nil)
		held, status := svc.HoldDeviceForFirmware(context.Background(), orgId, "dev", owner, false)
		require.True(t, held, status.Message)
		writes := st.device.deviceWrites

		held, status = svc.HoldDeviceForFirmware(context.Background(), orgId, "dev", owner, false)

		require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
		assert.True(t, held)
		assert.Equal(t, writes, st.device.deviceWrites)
	})

	t.Run("When the owner changed it should report the owner conflict and write nothing", func(t *testing.T) {
		st, _, svc := newTestHandler()
		orgId := uuid.New()
		holdTestDevice(t, st, orgId, lo.ToPtr("Fleet/other"), holdTestFirmware(), nil)
		writes := st.device.deviceWrites

		held, status := svc.HoldDeviceForFirmware(context.Background(), orgId, "dev", owner, true)

		require.Equal(t, int32(http.StatusConflict), status.Code)
		assert.False(t, held)
		assert.Equal(t, writes, st.device.deviceWrites)
	})

	t.Run("When the device does not exist it should report it as not found", func(t *testing.T) {
		_, _, svc := newTestHandler()

		held, status := svc.HoldDeviceForFirmware(context.Background(), uuid.New(), "dev", owner, true)

		require.Equal(t, int32(http.StatusNotFound), status.Code, status.Message)
		assert.False(t, held)
	})

	t.Run("When the device is being decommissioned it should report the conflict and write nothing", func(t *testing.T) {
		st, _, svc := newTestHandler()
		orgId := uuid.New()
		holdTestDevice(t, st, orgId, owner, holdTestFirmware(), nil)
		st.device.devices["dev"].Spec.Decommissioning = &domain.DeviceDecommission{Target: domain.DeviceDecommissionTargetTypeUnenroll}
		writes := st.device.deviceWrites

		held, status := svc.HoldDeviceForFirmware(context.Background(), orgId, "dev", owner, true)

		require.Equal(t, int32(http.StatusConflict), status.Code, status.Message)
		assert.False(t, held)
		assert.Equal(t, writes, st.device.deviceWrites)
	})
}
