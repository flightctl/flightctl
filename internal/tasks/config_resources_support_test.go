package tasks

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestConfigResourcesSupport(t *testing.T) {
	for _, support := range []struct {
		name  string
		value *bool
	}{
		{"absent", nil}, {"false", lo.ToPtr(false)}, {"true", lo.ToPtr(true)},
	} {
		for _, work := range []struct {
			name                string
			candidate, previous []domain.ConfigResource
		}{
			{"generated", []domain.ConfigResource{{}}, nil},
			{"cleanup", nil, []domain.ConfigResource{{}}},
			{"file-only", nil, nil},
			{"empty collections", []domain.ConfigResource{}, []domain.ConfigResource{}},
		} {
			t.Run("When support is "+support.name+" it should gate "+work.name, func(t *testing.T) {
				err := validateConfigResourcesSupport(support.value, work.candidate, work.previous)
				if !lo.FromPtr(support.value) && (len(work.candidate) > 0 || len(work.previous) > 0) {
					require.EqualError(t, err, "Device does not report support for registry configResources. Upgrade the agent before applying this configuration.")
				} else {
					require.NoError(t, err)
				}
			})
		}
	}
}

func TestRenderDevice_ConfigResourcesCapabilityReport(t *testing.T) {
	for _, support := range []struct {
		name  string
		value *bool
	}{{"absent", nil}, {"false", lo.ToPtr(false)}, {"true", lo.ToPtr(true)}} {
		for _, version := range []string{"0.0.1", "unknown", "1.5.0"} {
			for _, label := range []string{"false", "true"} {
				t.Run("When support is "+support.name+" it should ignore version "+version+" and label "+label, func(t *testing.T) {
					orgID := uuid.New()
					const name = "cleanup-device"
					ctrl := gomock.NewController(t)
					devices := deviceservice.NewMockService(ctrl)
					device := &domain.Device{
						Metadata: domain.ObjectMeta{
							Name:        lo.ToPtr(name),
							Annotations: &map[string]string{domain.DeviceAnnotationRenderedVersion: "1"},
							Labels:      &map[string]string{"configResourcesSupported": label},
						},
						Spec:   &domain.DeviceSpec{},
						Status: &domain.DeviceStatus{SystemInfo: domain.DeviceSystemInfo{ConfigResourcesSupported: support.value, AgentVersion: version}},
					}
					devices.EXPECT().GetDevice(gomock.Any(), orgID, name).Return(device, statusOK)
					if lo.FromPtr(support.value) {
						devices.EXPECT().OverwriteDeviceRepositoryRefs(gomock.Any(), orgID, name).Return(statusOK)
						devices.EXPECT().UpdateRenderedDevice(gomock.Any(), orgID, name, gomock.Any(), gomock.Any(), "", gomock.Any(), "", gomock.Any(), false, gomock.Any()).Return(statusOK)
					} else {
						previous := &domain.Device{Spec: &domain.DeviceSpec{ConfigResources: &[]domain.ConfigResource{{}}}}
						devices.EXPECT().GetRenderedDevice(gomock.Any(), orgID, name, domain.GetRenderedDeviceParams{}).Return(previous, statusOK)
						devices.EXPECT().UpdateDeviceAnnotations(gomock.Any(), orgID, name, gomock.Any(), gomock.Nil()).Return(statusOK)
						devices.EXPECT().SetDeviceServiceConditions(gomock.Any(), orgID, name, gomock.Any()).
							DoAndReturn(func(_ context.Context, _ uuid.UUID, _ string, conditions []domain.Condition) domain.Status {
								require.Equal(t, domain.ConditionStatusFalse, conditions[0].Status)
								require.Equal(t, "UnsupportedConfigResources", conditions[0].Reason)
								return statusOK
							})
						devices.EXPECT().UpdateServerSideDeviceStatus(gomock.Any(), orgID, name).Return(nil)
					}
					logic := NewDeviceRenderLogic(logrus.New(), devices, nil, nil, nil, newTestKVStore(), &config.Config{})
					err := logic.RenderDevice(context.Background(), orgID, createTestEvent(domain.DeviceKind, domain.EventReasonResourceUpdated, name))
					if lo.FromPtr(support.value) {
						require.NoError(t, err)
					} else {
						require.ErrorIs(t, err, errUnsupportedConfigResources)
					}
				})
			}
		}
	}
}

func TestRenderDevice_ConfigResourcesReadFailure(t *testing.T) {
	ctx := context.Background()
	orgID := uuid.New()
	const name = "resources-read-error"
	ctrl := gomock.NewController(t)
	devices := deviceservice.NewMockService(ctrl)
	device := &domain.Device{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr(name), Annotations: &map[string]string{domain.DeviceAnnotationRenderedVersion: "1"}},
		Spec:     &domain.DeviceSpec{},
	}
	devices.EXPECT().GetDevice(gomock.Any(), orgID, name).Return(device, statusOK)
	devices.EXPECT().GetRenderedDevice(gomock.Any(), orgID, name, domain.GetRenderedDeviceParams{}).
		Return(nil, domain.StatusInternalServerError("snapshot unavailable"))
	devices.EXPECT().SetDeviceServiceConditions(gomock.Any(), orgID, name, gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, _ string, conditions []domain.Condition) domain.Status {
			require.Equal(t, "Invalid", conditions[0].Reason)
			return statusOK
		})
	devices.EXPECT().UpdateServerSideDeviceStatus(gomock.Any(), orgID, name).Return(nil)
	logic := NewDeviceRenderLogic(logrus.New(), devices, nil, nil, nil, newTestKVStore(), &config.Config{})
	err := logic.RenderDevice(ctx, orgID, createTestEvent(domain.DeviceKind, domain.EventReasonResourceUpdated, name))
	require.ErrorContains(t, err, "snapshot unavailable")
}
