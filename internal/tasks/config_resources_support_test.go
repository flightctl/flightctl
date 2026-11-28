package tasks

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
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
			name      string
			candidate []domain.ConfigResource
		}{
			{"generated", []domain.ConfigResource{{}}},
			{"file-only", nil},
			{"empty collection", []domain.ConfigResource{}},
		} {
			t.Run("When support is "+support.name+" it should gate "+work.name, func(t *testing.T) {
				err := validateConfigResourcesSupport(support.value, work.candidate)
				if !lo.FromPtr(support.value) && len(work.candidate) > 0 {
					require.EqualError(t, err, "Device does not report support for registry configResources. Upgrade the agent before applying this configuration.")
				} else {
					require.NoError(t, err)
				}
			})
		}
	}
}

func TestRenderDevice_EmptyConfigResourcesCapabilityReport(t *testing.T) {
	for _, support := range []struct {
		name  string
		value *bool
	}{{"absent", nil}, {"false", lo.ToPtr(false)}, {"true", lo.ToPtr(true)}} {
		for _, version := range []string{"0.0.1", "unknown", "1.5.0"} {
			for _, label := range []string{"false", "true"} {
				t.Run("When support is "+support.name+" it should ignore version "+version+" and label "+label, func(t *testing.T) {
					orgID := uuid.New()
					const name = "file-only-device"
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
					devices.EXPECT().ReplaceDeviceRepositoryRefsIfCurrent(gomock.Any(), orgID, name, gomock.Any(), gomock.Any()).Return(common.RepositoryRefsReplaced, statusOK)
					devices.EXPECT().UpdateRenderedDevice(gomock.Any(), orgID, name, gomock.Any(), gomock.Any(), "", gomock.Any(), "", gomock.Any(), false, gomock.Any(), gomock.Any()).Return(statusOK)
					logic := NewDeviceRenderLogic(logrus.New(), devices, nil, nil, nil, newTestKVStore(), &config.Config{})
					err := logic.RenderDevice(context.Background(), orgID, createTestEvent(domain.DeviceKind, domain.EventReasonResourceUpdated, name))
					require.NoError(t, err)
				})
			}
		}
	}
}
