package tasks

import (
	"context"
	"net/http"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	eventservice "github.com/flightctl/flightctl/internal/service/event"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"
)

func TestFleetApplicationLifecycleLogic_SyncFleet(t *testing.T) {
	t.Run("When the fleet has a lifecycle default it caches it on every member device and emits a per-device event", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		orgId := uuid.New()
		log := logrus.New()
		fleetName := "test-fleet"
		event := lo.FromPtr(common.GetFleetApplicationLifecycleChangedEvent(context.Background(), fleetName, "app-1", domain.ApplicationLifecycleActionStop))

		fleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{
				Name:        lo.ToPtr(fleetName),
				Annotations: &map[string]string{domain.FleetAnnotationApplicationLifecycle: `{"app-1":{"desiredState":"stopped"}}`},
			},
		}

		mockFleetSvc := fleetservice.NewMockService(ctrl)
		mockDeviceSvc := deviceservice.NewMockService(ctrl)
		mockEventSvc := eventservice.NewMockService(ctrl)
		mockFleetSvc.EXPECT().GetFleet(gomock.Any(), orgId, fleetName, gomock.Any()).Return(fleet, domain.Status{Code: http.StatusOK})
		mockDeviceSvc.EXPECT().ListDevices(gomock.Any(), orgId, gomock.Any(), gomock.Any()).Return(&domain.DeviceList{
			Items: []domain.Device{
				*createTestDevice("device-1", "Fleet/"+fleetName),
				*createTestDevice("device-2", "Fleet/"+fleetName),
			},
		}, domain.Status{Code: http.StatusOK})

		mockDeviceSvc.EXPECT().UpdateDeviceAnnotations(gomock.Any(), orgId, "device-1",
			map[string]string{domain.DeviceAnnotationFleetApplicationLifecycle: `{"app-1":{"desiredState":"stopped"}}`}, []string(nil)).
			Return(domain.Status{Code: http.StatusOK})
		mockDeviceSvc.EXPECT().UpdateDeviceAnnotations(gomock.Any(), orgId, "device-2",
			map[string]string{domain.DeviceAnnotationFleetApplicationLifecycle: `{"app-1":{"desiredState":"stopped"}}`}, []string(nil)).
			Return(domain.Status{Code: http.StatusOK})

		mockEventSvc.EXPECT().CreateEvent(gomock.Any(), orgId, matchesDeviceApplicationLifecycleEvent("device-1", "app-1")).Times(1)
		mockEventSvc.EXPECT().CreateEvent(gomock.Any(), orgId, matchesDeviceApplicationLifecycleEvent("device-2", "app-1")).Times(1)

		logic := NewFleetApplicationLifecycleLogic(log, mockFleetSvc, mockDeviceSvc, mockEventSvc, orgId, event)
		require.NoError(t, logic.SyncFleet(context.Background()))
	})

	t.Run("When the fleet's lifecycle default was cleared it deletes the cached copy from every member device", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		orgId := uuid.New()
		log := logrus.New()
		fleetName := "test-fleet"
		event := lo.FromPtr(common.GetFleetApplicationLifecycleChangedEvent(context.Background(), fleetName, "app-1", domain.ApplicationLifecycleActionStart))

		fleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(fleetName)},
		}

		mockFleetSvc := fleetservice.NewMockService(ctrl)
		mockDeviceSvc := deviceservice.NewMockService(ctrl)
		mockEventSvc := eventservice.NewMockService(ctrl)
		mockFleetSvc.EXPECT().GetFleet(gomock.Any(), orgId, fleetName, gomock.Any()).Return(fleet, domain.Status{Code: http.StatusOK})
		mockDeviceSvc.EXPECT().ListDevices(gomock.Any(), orgId, gomock.Any(), gomock.Any()).Return(&domain.DeviceList{
			Items: []domain.Device{*createTestDevice("device-1", "Fleet/"+fleetName)},
		}, domain.Status{Code: http.StatusOK})

		mockDeviceSvc.EXPECT().UpdateDeviceAnnotations(gomock.Any(), orgId, "device-1",
			map[string]string{}, []string{domain.DeviceAnnotationFleetApplicationLifecycle}).
			Return(domain.Status{Code: http.StatusOK})
		mockEventSvc.EXPECT().CreateEvent(gomock.Any(), orgId, matchesDeviceApplicationLifecycleEvent("device-1", "app-1")).Times(1)

		logic := NewFleetApplicationLifecycleLogic(log, mockFleetSvc, mockDeviceSvc, mockEventSvc, orgId, event)
		require.NoError(t, logic.SyncFleet(context.Background()))
	})

	t.Run("When a device fails to sync it should still process the remaining devices and return an aggregate error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		orgId := uuid.New()
		log := logrus.New()
		fleetName := "test-fleet"
		event := lo.FromPtr(common.GetFleetApplicationLifecycleChangedEvent(context.Background(), fleetName, "app-1", domain.ApplicationLifecycleActionStop))

		fleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{
				Name:        lo.ToPtr(fleetName),
				Annotations: &map[string]string{domain.FleetAnnotationApplicationLifecycle: `{"app-1":{"desiredState":"stopped"}}`},
			},
		}

		mockFleetSvc := fleetservice.NewMockService(ctrl)
		mockDeviceSvc := deviceservice.NewMockService(ctrl)
		mockEventSvc := eventservice.NewMockService(ctrl)
		mockFleetSvc.EXPECT().GetFleet(gomock.Any(), orgId, fleetName, gomock.Any()).Return(fleet, domain.Status{Code: http.StatusOK})
		mockDeviceSvc.EXPECT().ListDevices(gomock.Any(), orgId, gomock.Any(), gomock.Any()).Return(&domain.DeviceList{
			Items: []domain.Device{
				*createTestDevice("device-1", "Fleet/"+fleetName),
				*createTestDevice("device-2", "Fleet/"+fleetName),
			},
		}, domain.Status{Code: http.StatusOK})

		mockDeviceSvc.EXPECT().UpdateDeviceAnnotations(gomock.Any(), orgId, "device-1", gomock.Any(), gomock.Any()).
			Return(domain.Status{Code: http.StatusInternalServerError, Message: "boom"})
		mockDeviceSvc.EXPECT().UpdateDeviceAnnotations(gomock.Any(), orgId, "device-2", gomock.Any(), gomock.Any()).
			Return(domain.Status{Code: http.StatusOK})
		mockEventSvc.EXPECT().CreateEvent(gomock.Any(), orgId, matchesDeviceApplicationLifecycleEvent("device-2", "app-1")).Times(1)

		logic := NewFleetApplicationLifecycleLogic(log, mockFleetSvc, mockDeviceSvc, mockEventSvc, orgId, event)
		err := logic.SyncFleet(context.Background())
		require.Error(t, err, "device-1's failure should surface as an error even though device-2 was processed")
	})

	t.Run("When the device list spans multiple pages it should process every page", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		orgId := uuid.New()
		log := logrus.New()
		fleetName := "test-fleet"
		event := lo.FromPtr(common.GetFleetApplicationLifecycleChangedEvent(context.Background(), fleetName, "app-1", domain.ApplicationLifecycleActionStop))

		fleet := &domain.Fleet{
			Metadata: domain.ObjectMeta{
				Name:        lo.ToPtr(fleetName),
				Annotations: &map[string]string{domain.FleetAnnotationApplicationLifecycle: `{"app-1":{"desiredState":"stopped"}}`},
			},
		}

		mockFleetSvc := fleetservice.NewMockService(ctrl)
		mockDeviceSvc := deviceservice.NewMockService(ctrl)
		mockEventSvc := eventservice.NewMockService(ctrl)
		mockFleetSvc.EXPECT().GetFleet(gomock.Any(), orgId, fleetName, gomock.Any()).Return(fleet, domain.Status{Code: http.StatusOK})

		firstPageContinue := "page-2"
		gomock.InOrder(
			mockDeviceSvc.EXPECT().ListDevices(gomock.Any(), orgId, gomock.Any(), gomock.Any()).Return(&domain.DeviceList{
				Metadata: domain.ListMeta{Continue: &firstPageContinue},
				Items:    []domain.Device{*createTestDevice("device-1", "Fleet/"+fleetName)},
			}, domain.Status{Code: http.StatusOK}),
			mockDeviceSvc.EXPECT().ListDevices(gomock.Any(), orgId, gomock.Any(), gomock.Any()).Return(&domain.DeviceList{
				Items: []domain.Device{*createTestDevice("device-2", "Fleet/"+fleetName)},
			}, domain.Status{Code: http.StatusOK}),
		)

		mockDeviceSvc.EXPECT().UpdateDeviceAnnotations(gomock.Any(), orgId, "device-1", gomock.Any(), gomock.Any()).Return(domain.Status{Code: http.StatusOK})
		mockDeviceSvc.EXPECT().UpdateDeviceAnnotations(gomock.Any(), orgId, "device-2", gomock.Any(), gomock.Any()).Return(domain.Status{Code: http.StatusOK})
		mockEventSvc.EXPECT().CreateEvent(gomock.Any(), orgId, matchesDeviceApplicationLifecycleEvent("device-1", "app-1")).Times(1)
		mockEventSvc.EXPECT().CreateEvent(gomock.Any(), orgId, matchesDeviceApplicationLifecycleEvent("device-2", "app-1")).Times(1)

		logic := NewFleetApplicationLifecycleLogic(log, mockFleetSvc, mockDeviceSvc, mockEventSvc, orgId, event)
		require.NoError(t, logic.SyncFleet(context.Background()))
	})

	t.Run("When the event has no details it should return an error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		orgId := uuid.New()
		log := logrus.New()
		fleetName := "test-fleet"
		event := domain.Event{
			InvolvedObject: domain.ObjectReference{Kind: domain.FleetKind, Name: fleetName},
			Reason:         domain.EventReasonApplicationLifecycleChanged,
		}

		fleet := &domain.Fleet{Metadata: domain.ObjectMeta{Name: lo.ToPtr(fleetName)}}
		mockFleetSvc := fleetservice.NewMockService(ctrl)
		mockDeviceSvc := deviceservice.NewMockService(ctrl)
		mockEventSvc := eventservice.NewMockService(ctrl)
		mockFleetSvc.EXPECT().GetFleet(gomock.Any(), orgId, fleetName, gomock.Any()).Return(fleet, domain.Status{Code: http.StatusOK})

		logic := NewFleetApplicationLifecycleLogic(log, mockFleetSvc, mockDeviceSvc, mockEventSvc, orgId, event)
		require.Error(t, logic.SyncFleet(context.Background()))
	})
}

func matchesDeviceApplicationLifecycleEvent(deviceName string, appName string) gomock.Matcher {
	return gomock.Cond(func(x any) bool {
		e, ok := x.(*domain.Event)
		if !ok || e == nil {
			return false
		}
		if e.InvolvedObject.Kind != domain.DeviceKind || e.InvolvedObject.Name != deviceName {
			return false
		}
		details, err := e.Details.AsApplicationLifecycleChangedDetails()
		if err != nil {
			return false
		}
		return details.AppName == appName
	})
}
