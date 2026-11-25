package model

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestDeviceToApiResourceRenderedOneTimeWindows(t *testing.T) {
	newOneTimeSchedule := func() *domain.UpdateSchedule {
		var schedule domain.UpdateSchedule
		require.NoError(t, json.Unmarshal([]byte(`{"oneTimeWindows":[{"date":"2030-01-02","startTime":"03:04","startGraceDuration":"1h"}]}`), &schedule))
		return &schedule
	}
	spec := domain.DeviceSpec{
		UpdatePolicy: &domain.DeviceUpdatePolicySpec{
			DownloadSchedule: newOneTimeSchedule(),
			UpdateSchedule:   newOneTimeSchedule(),
		},
	}

	newDevice := func(eligible *bool) *Device {
		return &Device{
			Resource: Resource{
				Name:        "device",
				Annotations: JSONMap[string, string]{domain.DeviceAnnotationRenderedVersion: "1"},
			},
			Spec:   MakeJSONField(spec),
			Status: MakeJSONField(domain.DeviceStatus{SystemInfo: domain.DeviceSystemInfo{OneTimeWindowsEligible: eligible}}),
		}
	}

	t.Run("When the device does not support one-time windows it should omit them from the rendered response", func(t *testing.T) {
		resource, err := newDevice(lo.ToPtr(false)).ToApiResource(WithRendered(context.Background(), nil))
		require.NoError(t, err)
		require.NotNil(t, resource.Spec.UpdatePolicy)
		require.Nil(t, resource.Spec.UpdatePolicy.DownloadSchedule)
		require.Nil(t, resource.Spec.UpdatePolicy.UpdateSchedule)
		require.NotNil(t, newDevice(lo.ToPtr(false)).Spec.Data.UpdatePolicy.DownloadSchedule.OneTimeWindows)
	})

	t.Run("When the device supports one-time windows it should preserve them in the rendered response", func(t *testing.T) {
		resource, err := newDevice(lo.ToPtr(true)).ToApiResource(WithRendered(context.Background(), nil))
		require.NoError(t, err)
		require.NotNil(t, resource.Spec.UpdatePolicy)
		require.NotNil(t, resource.Spec.UpdatePolicy.DownloadSchedule.OneTimeWindows)
		require.NotNil(t, resource.Spec.UpdatePolicy.UpdateSchedule.OneTimeWindows)
	})

	t.Run("When the device does not report the capability it should omit one-time windows", func(t *testing.T) {
		resource, err := newDevice(nil).ToApiResource(WithRendered(context.Background(), nil))
		require.NoError(t, err)
		require.NotNil(t, resource.Spec.UpdatePolicy)
		require.Nil(t, resource.Spec.UpdatePolicy.DownloadSchedule)
		require.Nil(t, resource.Spec.UpdatePolicy.UpdateSchedule)
	})
}
