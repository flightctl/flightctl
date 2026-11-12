package tasks

import (
	"context"
	"errors"
	"net/http"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
	dependencyrefservice "github.com/flightctl/flightctl/internal/service/dependencyref"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	templateversionservice "github.com/flightctl/flightctl/internal/service/templateversion"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"
)

func TestUpdateDeviceToFleetTemplateFirmwareHold(t *testing.T) {
	const (
		owner      = "Fleet/test-fleet"
		deviceName = "test-device"
	)
	firmware := func() *domain.FirmwareSpec {
		return &domain.FirmwareSpec{
			Profile:    "board-a",
			Updater:    domain.FirmwareUpdaterSpec{Name: "vendor-updater", Version: "1.0.0", Config: map[string]interface{}{}},
			Components: []api.FirmwareComponentSpec{{Name: "bios", Version: api.FirmwareVersionConstraints{Exact: lo.ToPtr("2.0")}}},
			Overlays:   []api.FirmwareOverlaySpec{},
		}
	}
	heldStatus := domain.NewFailureStatus(http.StatusConflict, domain.DeviceSpecValidReasonFirmwareUnsupported, "device is held")
	setup := func(t *testing.T) (*deviceservice.MockService, FleetRolloutsLogic) {
		ctrl := gomock.NewController(t)
		devices := deviceservice.NewMockService(ctrl)
		logic := NewFleetRolloutsLogic(logrus.New(), fleetservice.NewMockService(ctrl), templateversionservice.NewMockService(ctrl), devices,
			dependencyrefservice.NewMockService(ctrl), uuid.New(), createTestEvent(domain.DeviceKind, domain.EventReasonResourceUpdated, deviceName))
		logic.owner = owner
		return devices, logic
	}

	t.Run("When the device has desired firmware it should record the hold without replacing its spec", func(t *testing.T) {
		devices, logic := setup(t)
		device := createTestDevice(deviceName, owner)
		device.Spec.Firmware = firmware()
		devices.EXPECT().HoldDeviceForFirmware(gomock.Any(), logic.orgId, deviceName, lo.ToPtr(owner), false).Return(true, domain.StatusOK())

		refs, err := logic.updateDeviceToFleetTemplate(context.Background(), device, createTestTemplateVersion("tv-1"), false)

		require.Error(t, err)
		assert.True(t, errors.Is(err, errFirmwareHold))
		assert.Empty(t, refs)
	})

	t.Run("When the device has an outstanding attempt it should record the hold without replacing its spec", func(t *testing.T) {
		devices, logic := setup(t)
		device := createTestDevice(deviceName, owner)
		device.Status.Firmware = &domain.FirmwareStatus{Attempt: &domain.FirmwareAttemptStatus{Id: "attempt-1", Phase: domain.FirmwareAttemptPhaseStaging}}
		devices.EXPECT().HoldDeviceForFirmware(gomock.Any(), logic.orgId, deviceName, lo.ToPtr(owner), false).Return(true, domain.StatusOK())

		_, err := logic.updateDeviceToFleetTemplate(context.Background(), device, createTestTemplateVersion("tv-1"), false)

		assert.True(t, errors.Is(err, errFirmwareHold))
	})

	t.Run("When the template version carries firmware it should hold a device without firmware state", func(t *testing.T) {
		devices, logic := setup(t)
		templateVersion := createTestTemplateVersion("tv-1")
		templateVersion.Status.Firmware = firmware()
		devices.EXPECT().HoldDeviceForFirmware(gomock.Any(), logic.orgId, deviceName, lo.ToPtr(owner), true).Return(true, domain.StatusOK())

		_, err := logic.updateDeviceToFleetTemplate(context.Background(), createTestDevice(deviceName, owner), templateVersion, false)

		assert.True(t, errors.Is(err, errFirmwareHold))
	})

	t.Run("When the device no longer needs a hold it should roll out the freshly read device", func(t *testing.T) {
		devices, logic := setup(t)
		stale := createTestDevice(deviceName, owner)
		stale.Status.Firmware = &domain.FirmwareStatus{Attempt: &domain.FirmwareAttemptStatus{Id: "attempt-1", Phase: domain.FirmwareAttemptPhaseVerifying}}
		fresh := createTestDevice(deviceName, owner)
		fresh.Status.Firmware = &domain.FirmwareStatus{Attempt: &domain.FirmwareAttemptStatus{Id: "attempt-1", Phase: domain.FirmwareAttemptPhaseSucceeded}}
		gomock.InOrder(
			devices.EXPECT().HoldDeviceForFirmware(gomock.Any(), logic.orgId, deviceName, lo.ToPtr(owner), false).Return(false, domain.StatusOK()),
			devices.EXPECT().GetDevice(gomock.Any(), logic.orgId, deviceName).Return(fresh, domain.StatusOK()),
			devices.EXPECT().ReplaceDeviceSpec(gomock.Any(), logic.orgId, deviceName, lo.ToPtr(owner), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(fresh, domain.StatusOK()),
		)

		_, err := logic.updateDeviceToFleetTemplate(context.Background(), stale, createTestTemplateVersion("tv-1"), false)

		require.NoError(t, err)
	})

	t.Run("When the spec write reports a hold it should stop without retrying", func(t *testing.T) {
		devices, logic := setup(t)
		devices.EXPECT().ReplaceDeviceSpec(gomock.Any(), logic.orgId, deviceName, lo.ToPtr(owner), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, heldStatus).Times(1)

		_, err := logic.updateDeviceToFleetTemplate(context.Background(), createTestDevice(deviceName, owner), createTestTemplateVersion("tv-1"), false)

		require.Error(t, err)
		assert.True(t, errors.Is(err, errFirmwareHold))
	})

	t.Run("When recording the hold fails it should return the failure", func(t *testing.T) {
		devices, logic := setup(t)
		device := createTestDevice(deviceName, owner)
		device.Spec.Firmware = firmware()
		devices.EXPECT().HoldDeviceForFirmware(gomock.Any(), logic.orgId, deviceName, lo.ToPtr(owner), false).Return(false, domain.StatusInternalServerError("database unavailable"))

		_, err := logic.updateDeviceToFleetTemplate(context.Background(), device, createTestTemplateVersion("tv-1"), false)

		require.ErrorContains(t, err, "database unavailable")
		assert.False(t, errors.Is(err, errFirmwareHold))
	})
}

func TestRolloutDeviceFirmwareHold(t *testing.T) {
	t.Run("When the device is held it should not fail the rollout and keep its dependency references", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		orgID := uuid.New()
		devices := deviceservice.NewMockService(ctrl)
		fleets := fleetservice.NewMockService(ctrl)
		templateVersions := templateversionservice.NewMockService(ctrl)
		// The dependency reference service has no expectations: replacing the
		// device's references would fail the test.
		logic := NewFleetRolloutsLogic(logrus.New(), fleets, templateVersions, devices, dependencyrefservice.NewMockService(ctrl), orgID,
			createTestEvent(domain.DeviceKind, domain.EventReasonResourceUpdated, "test-device"))
		device := createTestDevice("test-device", "Fleet/test-fleet")
		device.Status.Firmware = &domain.FirmwareStatus{Attempt: &domain.FirmwareAttemptStatus{Id: "attempt-1", Phase: domain.FirmwareAttemptPhaseStaging}}
		devices.EXPECT().GetDevice(gomock.Any(), orgID, "test-device").Return(device, domain.StatusOK())
		templateVersions.EXPECT().GetLatestTemplateVersion(gomock.Any(), orgID, "test-fleet").Return(createTestTemplateVersion("tv-1"), domain.StatusOK())
		fleets.EXPECT().GetFleet(gomock.Any(), orgID, "test-fleet", gomock.Any()).Return(createTestFleetForRollout("test-fleet", nil), domain.StatusOK())
		devices.EXPECT().HoldDeviceForFirmware(gomock.Any(), orgID, "test-device", lo.ToPtr("Fleet/test-fleet"), false).Return(true, domain.StatusOK())

		require.NoError(t, logic.RolloutDevice(context.Background()))
	})
}
