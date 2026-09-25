package tasks

import (
	"context"
	"net/http"
	"testing"

	v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func newTestFleetSelectorLogic(t *testing.T, deviceSvc deviceservice.Service, fleetSvc fleetservice.Service, event domain.Event) FleetSelectorMatchingLogic {
	t.Helper()
	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)
	return FleetSelectorMatchingLogic{
		log:          log,
		deviceSvc:    deviceSvc,
		fleetSvc:     fleetSvc,
		orgId:        uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		event:        event,
		itemsPerPage: 100,
	}
}

func makeDeviceWithEnrollmentHooks(name string, labels map[string]string, owner string, condStatus domain.ConditionStatus, condReason string) domain.Device {
	device := domain.Device{
		Metadata: domain.ObjectMeta{
			Name:   lo.ToPtr(name),
			Labels: &labels,
		},
		Status: &domain.DeviceStatus{
			Conditions: []domain.Condition{
				{
					Type:   domain.ConditionTypeDeviceEnrollmentHooks,
					Status: condStatus,
					Reason: condReason,
				},
			},
		},
	}
	if owner != "" {
		device.Metadata.Owner = util.SetResourceOwner(domain.FleetKind, owner)
	}
	return device
}

func makeDeviceWithoutEnrollmentHooks(name string, labels map[string]string, owner string) domain.Device {
	device := domain.Device{
		Metadata: domain.ObjectMeta{
			Name:   lo.ToPtr(name),
			Labels: &labels,
		},
		Status: &domain.DeviceStatus{
			Conditions: []domain.Condition{},
		},
	}
	if owner != "" {
		device.Metadata.Owner = util.SetResourceOwner(domain.FleetKind, owner)
	}
	return device
}

func TestDeviceLabelsUpdated_EnrollmentHooksGate(t *testing.T) {
	orgId := uuid.MustParse("00000000-0000-0000-0000-000000000001")

	t.Run("When device has EnrollmentHooks False it should skip fleet matching", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockDeviceSvc := deviceservice.NewMockService(ctrl)
		mockFleetSvc := fleetservice.NewMockService(ctrl)

		deviceName := "gated-device"
		gatedDevice := makeDeviceWithEnrollmentHooks(deviceName, map[string]string{"env": "prod"}, "my-fleet",
			domain.ConditionStatusFalse, v1beta1.EnrollmentHooksReasonPending)

		event := domain.Event{
			InvolvedObject: domain.ObjectReference{
				Kind: domain.DeviceKind,
				Name: deviceName,
			},
		}

		// GetDevice is called, returns a gated device
		mockDeviceSvc.EXPECT().GetDevice(gomock.Any(), orgId, deviceName).
			Return(&gatedDevice, domain.StatusOK())

		// No further calls expected — the gate short-circuits
		logic := newTestFleetSelectorLogic(t, mockDeviceSvc, mockFleetSvc, event)
		err := logic.DeviceLabelsUpdated(context.Background())
		require.NoError(t, err)
	})

	t.Run("When device has EnrollmentHooks True it should proceed with fleet matching", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockDeviceSvc := deviceservice.NewMockService(ctrl)
		mockFleetSvc := fleetservice.NewMockService(ctrl)

		deviceName := "ungated-device"
		labels := map[string]string{"env": "prod"}
		ungatedDevice := makeDeviceWithEnrollmentHooks(deviceName, labels, "my-fleet",
			domain.ConditionStatusTrue, v1beta1.EnrollmentHooksReasonSucceeded)

		event := domain.Event{
			InvolvedObject: domain.ObjectReference{
				Kind: domain.DeviceKind,
				Name: deviceName,
			},
		}

		// GetDevice returns an ungated device with a fleet owner
		mockDeviceSvc.EXPECT().GetDevice(gomock.Any(), orgId, deviceName).
			Return(&ungatedDevice, domain.StatusOK())

		// Device is not gated, so fleet matching proceeds.
		// ListFleets is called to find matching fleets.
		mockFleetSvc.EXPECT().ListFleets(gomock.Any(), orgId, gomock.Any()).
			Return(&domain.FleetList{
				Items: []domain.Fleet{
					{
						Metadata: domain.ObjectMeta{Name: lo.ToPtr("my-fleet")},
						Spec:     domain.FleetSpec{Selector: &domain.LabelSelector{MatchLabels: &labels}},
					},
				},
			}, domain.StatusOK())

		// Single fleet matches current owner — no owner update needed.
		// MultipleOwners condition message unchanged (both empty) — no condition update call.

		logic := newTestFleetSelectorLogic(t, mockDeviceSvc, mockFleetSvc, event)
		err := logic.DeviceLabelsUpdated(context.Background())
		require.NoError(t, err)
	})

	t.Run("When device has no EnrollmentHooks condition it should proceed with fleet matching", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockDeviceSvc := deviceservice.NewMockService(ctrl)
		mockFleetSvc := fleetservice.NewMockService(ctrl)

		deviceName := "no-hooks-device"
		labels := map[string]string{"env": "prod"}
		device := makeDeviceWithoutEnrollmentHooks(deviceName, labels, "my-fleet")

		event := domain.Event{
			InvolvedObject: domain.ObjectReference{
				Kind: domain.DeviceKind,
				Name: deviceName,
			},
		}

		mockDeviceSvc.EXPECT().GetDevice(gomock.Any(), orgId, deviceName).
			Return(&device, domain.StatusOK())

		// No EnrollmentHooks condition, so proceeds to fleet matching
		mockFleetSvc.EXPECT().ListFleets(gomock.Any(), orgId, gomock.Any()).
			Return(&domain.FleetList{
				Items: []domain.Fleet{
					{
						Metadata: domain.ObjectMeta{Name: lo.ToPtr("my-fleet")},
						Spec:     domain.FleetSpec{Selector: &domain.LabelSelector{MatchLabels: &labels}},
					},
				},
			}, domain.StatusOK())

		// Single fleet matches current owner — no owner update or condition update needed.

		logic := newTestFleetSelectorLogic(t, mockDeviceSvc, mockFleetSvc, event)
		err := logic.DeviceLabelsUpdated(context.Background())
		require.NoError(t, err)
	})
}

func TestFleetSelectorUpdated_EnrollmentHooksGate(t *testing.T) {
	orgId := uuid.MustParse("00000000-0000-0000-0000-000000000001")

	t.Run("When fleet selector matches gated and ungated devices it should skip gated devices", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockDeviceSvc := deviceservice.NewMockService(ctrl)
		mockFleetSvc := fleetservice.NewMockService(ctrl)

		fleetName := "test-fleet"
		labels := map[string]string{"env": "prod"}

		fleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(fleetName)},
			Spec:     domain.FleetSpec{Selector: &domain.LabelSelector{MatchLabels: &labels}},
		}

		// Gated device — should be skipped by the primary gate
		gatedDevice := makeDeviceWithEnrollmentHooks("gated-dev", labels, "",
			domain.ConditionStatusFalse, v1beta1.EnrollmentHooksReasonPending)

		// Ungated device — should be processed normally
		ungatedDevice := makeDeviceWithEnrollmentHooks("ungated-dev", labels, "",
			domain.ConditionStatusTrue, v1beta1.EnrollmentHooksReasonSucceeded)

		event := domain.Event{
			InvolvedObject: domain.ObjectReference{
				Kind: domain.FleetKind,
				Name: fleetName,
			},
		}

		// GetFleet returns the fleet
		mockFleetSvc.EXPECT().GetFleet(gomock.Any(), orgId, fleetName, gomock.Any()).
			Return(fleet, domain.StatusOK())

		// handleOrphanedDevices: list devices owned by this fleet that don't match
		mockDeviceSvc.EXPECT().ListDevices(gomock.Any(), orgId, gomock.Any(), gomock.Any()).
			Return(&domain.DeviceList{Items: []domain.Device{}}, domain.StatusOK()).
			Times(1)

		// handleDevicesMatchingFleet: list devices matching the fleet's labels —
		// returns both gated and ungated
		mockDeviceSvc.EXPECT().ListDevices(gomock.Any(), orgId, gomock.Any(), gomock.Any()).
			Return(&domain.DeviceList{
				Items: []domain.Device{gatedDevice, ungatedDevice},
			}, domain.StatusOK()).
			Times(1)

		// ListFleets is called (lazy fetcher) for recomputing ownership of ungated device
		mockFleetSvc.EXPECT().ListFleets(gomock.Any(), orgId, gomock.Any()).
			Return(&domain.FleetList{Items: []domain.Fleet{*fleet}}, domain.StatusOK())

		// Ungated device: recomputeDeviceOwnership sets owner (no existing owner → new owner)
		mockDeviceSvc.EXPECT().SetDeviceOwner(gomock.Any(), orgId, "ungated-dev", gomock.Any(), gomock.Any()).
			Return(&ungatedDevice, domain.StatusOK())
		mockDeviceSvc.EXPECT().UpdateServerSideDeviceStatus(gomock.Any(), orgId, "ungated-dev").
			Return(nil)
		// MultipleOwners condition message unchanged (both empty) — no condition update call.

		// handleDevicesWithMultipleOwnersCondition: no devices with condition
		mockDeviceSvc.EXPECT().ListDevicesByServiceCondition(gomock.Any(), orgId,
			string(domain.ConditionTypeDeviceMultipleOwners), string(domain.ConditionStatusTrue), gomock.Any()).
			Return(&domain.DeviceList{Items: []domain.Device{}}, domain.StatusOK())

		logic := newTestFleetSelectorLogic(t, mockDeviceSvc, mockFleetSvc, event)
		err := logic.FleetSelectorUpdated(context.Background())
		require.NoError(t, err)

		// The gated device should not have any SetDeviceOwner or
		// SetDeviceServiceConditions calls — verified by gomock
		// (any unexpected call would fail the test).
	})

	t.Run("When an orphaned device has EnrollmentHooks False it should skip ownership recomputation", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockDeviceSvc := deviceservice.NewMockService(ctrl)
		mockFleetSvc := fleetservice.NewMockService(ctrl)

		fleetName := "test-fleet"
		labels := map[string]string{"env": "prod"}
		fleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(fleetName)},
			Spec:     domain.FleetSpec{Selector: &domain.LabelSelector{MatchLabels: &labels}},
		}
		gatedDevice := makeDeviceWithEnrollmentHooks("gated-orphaned-dev", map[string]string{"env": "dev"}, fleetName,
			domain.ConditionStatusFalse, v1beta1.EnrollmentHooksReasonPending)

		event := domain.Event{
			InvolvedObject: domain.ObjectReference{
				Kind: domain.FleetKind,
				Name: fleetName,
			},
		}

		gomock.InOrder(
			mockFleetSvc.EXPECT().GetFleet(gomock.Any(), orgId, fleetName, gomock.Any()).
				Return(fleet, domain.StatusOK()),
			mockDeviceSvc.EXPECT().ListDevices(gomock.Any(), orgId, gomock.Any(), gomock.Any()).
				Return(&domain.DeviceList{Items: []domain.Device{gatedDevice}}, domain.StatusOK()),
			mockDeviceSvc.EXPECT().ListDevices(gomock.Any(), orgId, gomock.Any(), gomock.Any()).
				Return(&domain.DeviceList{Items: []domain.Device{}}, domain.StatusOK()),
			mockDeviceSvc.EXPECT().ListDevicesByServiceCondition(gomock.Any(), orgId,
				string(domain.ConditionTypeDeviceMultipleOwners), string(domain.ConditionStatusTrue), gomock.Any()).
				Return(&domain.DeviceList{Items: []domain.Device{}}, domain.StatusOK()),
		)

		logic := newTestFleetSelectorLogic(t, mockDeviceSvc, mockFleetSvc, event)
		err := logic.FleetSelectorUpdated(context.Background())
		require.NoError(t, err)
	})

	t.Run("When a multiple-owner device has EnrollmentHooks False it should skip ownership recomputation", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockDeviceSvc := deviceservice.NewMockService(ctrl)
		mockFleetSvc := fleetservice.NewMockService(ctrl)

		fleetName := "test-fleet"
		labels := map[string]string{"env": "prod"}
		fleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(fleetName)},
			Spec:     domain.FleetSpec{Selector: &domain.LabelSelector{MatchLabels: &labels}},
		}
		gatedDevice := makeDeviceWithEnrollmentHooks("gated-multiple-owner-dev", labels, fleetName,
			domain.ConditionStatusFalse, v1beta1.EnrollmentHooksReasonPending)
		gatedDevice.Status.Conditions = append(gatedDevice.Status.Conditions, domain.Condition{
			Type:   domain.ConditionTypeDeviceMultipleOwners,
			Status: domain.ConditionStatusTrue,
		})

		event := domain.Event{
			InvolvedObject: domain.ObjectReference{
				Kind: domain.FleetKind,
				Name: fleetName,
			},
		}

		gomock.InOrder(
			mockFleetSvc.EXPECT().GetFleet(gomock.Any(), orgId, fleetName, gomock.Any()).
				Return(fleet, domain.StatusOK()),
			mockDeviceSvc.EXPECT().ListDevices(gomock.Any(), orgId, gomock.Any(), gomock.Any()).
				Return(&domain.DeviceList{Items: []domain.Device{}}, domain.StatusOK()),
			mockDeviceSvc.EXPECT().ListDevices(gomock.Any(), orgId, gomock.Any(), gomock.Any()).
				Return(&domain.DeviceList{Items: []domain.Device{}}, domain.StatusOK()),
			mockDeviceSvc.EXPECT().ListDevicesByServiceCondition(gomock.Any(), orgId,
				string(domain.ConditionTypeDeviceMultipleOwners), string(domain.ConditionStatusTrue), gomock.Any()).
				Return(&domain.DeviceList{Items: []domain.Device{gatedDevice}}, domain.StatusOK()),
		)

		logic := newTestFleetSelectorLogic(t, mockDeviceSvc, mockFleetSvc, event)
		err := logic.FleetSelectorUpdated(context.Background())
		require.NoError(t, err)
	})

	t.Run("When a gated device is owned by a deleted or empty-selector fleet it should defer ownership recomputation", func(t *testing.T) {
		fleetName := "test-fleet"
		gatedDevice := makeDeviceWithEnrollmentHooks("gated-owned-dev", map[string]string{"env": "prod"}, fleetName,
			domain.ConditionStatusFalse, v1beta1.EnrollmentHooksReasonPending)

		tests := []struct {
			name        string
			fleet       *domain.Fleet
			fleetStatus domain.Status
		}{
			{
				name:        "deleted fleet",
				fleetStatus: domain.Status{Code: http.StatusNotFound},
			},
			{
				name: "empty selector",
				fleet: &domain.Fleet{
					Metadata: domain.ObjectMeta{Name: lo.ToPtr(fleetName)},
				},
				fleetStatus: domain.StatusOK(),
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()

				mockDeviceSvc := deviceservice.NewMockService(ctrl)
				mockFleetSvc := fleetservice.NewMockService(ctrl)
				event := domain.Event{
					InvolvedObject: domain.ObjectReference{Kind: domain.FleetKind, Name: fleetName},
				}

				mockFleetSvc.EXPECT().GetFleet(gomock.Any(), orgId, fleetName, gomock.Any()).
					Return(tt.fleet, tt.fleetStatus)
				mockDeviceSvc.EXPECT().ListDevices(gomock.Any(), orgId, gomock.Any(), gomock.Any()).
					Return(&domain.DeviceList{Items: []domain.Device{gatedDevice}}, domain.StatusOK())
				mockDeviceSvc.EXPECT().ListDevicesByServiceCondition(gomock.Any(), orgId,
					string(domain.ConditionTypeDeviceMultipleOwners), string(domain.ConditionStatusTrue), gomock.Any()).
					Return(&domain.DeviceList{Items: []domain.Device{}}, domain.StatusOK())

				logic := newTestFleetSelectorLogic(t, mockDeviceSvc, mockFleetSvc, event)
				err := logic.FleetSelectorUpdated(context.Background())
				require.NoError(t, err)
			})
		}
	})

	t.Run("When device not found it should return nil (no error)", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockDeviceSvc := deviceservice.NewMockService(ctrl)
		mockFleetSvc := fleetservice.NewMockService(ctrl)

		event := domain.Event{
			InvolvedObject: domain.ObjectReference{
				Kind: domain.DeviceKind,
				Name: "deleted-device",
			},
		}

		mockDeviceSvc.EXPECT().GetDevice(gomock.Any(), orgId, "deleted-device").
			Return(nil, domain.Status{Code: http.StatusNotFound})

		logic := newTestFleetSelectorLogic(t, mockDeviceSvc, mockFleetSvc, event)
		err := logic.DeviceLabelsUpdated(context.Background())
		require.NoError(t, err)
	})
}
