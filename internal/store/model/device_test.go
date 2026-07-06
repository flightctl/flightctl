package model

import (
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestRenderedContainerApp(t *testing.T, name string) domain.ApplicationProviderSpec {
	t.Helper()
	containerApp := domain.ContainerApplication{
		AppType: domain.AppTypeContainer,
		Name:    lo.ToPtr(name),
		Image:   "quay.io/test/app:v1",
	}
	var app domain.ApplicationProviderSpec
	require.NoError(t, app.FromContainerApplication(containerApp))
	return app
}

func newTestRenderedDevice(t *testing.T, annotations map[string]string, apps []domain.ApplicationProviderSpec) *Device {
	t.Helper()
	return &Device{
		Resource: Resource{
			Name:        "test-device",
			Annotations: MakeJSONMap(annotations),
		},
		Spec:                 MakeJSONField(domain.DeviceSpec{}),
		Status:               MakeJSONField(domain.NewDeviceStatus()),
		RenderedConfig:       MakeJSONField[*[]domain.ConfigProviderSpec](nil),
		RenderedApplications: MakeJSONField(&apps),
	}
}

func TestDeviceToApiResource_ApplicationLifecycleOverlay(t *testing.T) {
	t.Run("When device has no lifecycle annotation the rendered applications should be unchanged", func(t *testing.T) {
		apps := []domain.ApplicationProviderSpec{newTestRenderedContainerApp(t, "app-1")}
		d := newTestRenderedDevice(t, map[string]string{
			domain.DeviceAnnotationRenderedVersion: "1",
		}, apps)

		resource, err := d.ToApiResource(WithRendered(nil))
		require.NoError(t, err)
		require.NotNil(t, resource.Spec.Applications)
		require.Len(t, *resource.Spec.Applications, 1)

		containerApp, err := (*resource.Spec.Applications)[0].AsContainerApplication()
		require.NoError(t, err)
		assert.Nil(t, containerApp.DesiredState)
	})

	t.Run("When device has a lifecycle annotation it should overlay the matching rendered application", func(t *testing.T) {
		apps := []domain.ApplicationProviderSpec{
			newTestRenderedContainerApp(t, "app-1"),
			newTestRenderedContainerApp(t, "app-2"),
		}
		d := newTestRenderedDevice(t, map[string]string{
			domain.DeviceAnnotationRenderedVersion:      "1",
			domain.DeviceAnnotationApplicationLifecycle: `{"app-1":{"desiredState":"stopped","restartGeneration":5}}`,
		}, apps)

		resource, err := d.ToApiResource(WithRendered(nil))
		require.NoError(t, err)
		require.NotNil(t, resource.Spec.Applications)
		require.Len(t, *resource.Spec.Applications, 2)

		byName := map[string]domain.ApplicationProviderSpec{}
		for _, app := range *resource.Spec.Applications {
			name, err := app.GetName()
			require.NoError(t, err)
			byName[*name] = app
		}

		app1, err := byName["app-1"].AsContainerApplication()
		require.NoError(t, err)
		require.NotNil(t, app1.DesiredState)
		assert.Equal(t, domain.ApplicationDesiredStateStopped, *app1.DesiredState)
		require.NotNil(t, app1.RestartGeneration)
		assert.Equal(t, 5, *app1.RestartGeneration)

		app2, err := byName["app-2"].AsContainerApplication()
		require.NoError(t, err)
		assert.Nil(t, app2.DesiredState, "app-2 has no override and should be untouched")
	})

	t.Run("When the lifecycle annotation is invalid JSON it should return an error", func(t *testing.T) {
		apps := []domain.ApplicationProviderSpec{newTestRenderedContainerApp(t, "app-1")}
		d := newTestRenderedDevice(t, map[string]string{
			domain.DeviceAnnotationRenderedVersion:      "1",
			domain.DeviceAnnotationApplicationLifecycle: `not-json`,
		}, apps)

		_, err := d.ToApiResource(WithRendered(nil))
		require.Error(t, err)
	})

	t.Run("When the lifecycle annotation is an empty string it should be ignored", func(t *testing.T) {
		apps := []domain.ApplicationProviderSpec{newTestRenderedContainerApp(t, "app-1")}
		d := newTestRenderedDevice(t, map[string]string{
			domain.DeviceAnnotationRenderedVersion:      "1",
			domain.DeviceAnnotationApplicationLifecycle: "",
		}, apps)

		resource, err := d.ToApiResource(WithRendered(nil))
		require.NoError(t, err)
		require.NotNil(t, resource.Spec.Applications)
		require.Len(t, *resource.Spec.Applications, 1)
		containerApp, err := (*resource.Spec.Applications)[0].AsContainerApplication()
		require.NoError(t, err)
		assert.Nil(t, containerApp.DesiredState)
	})

	t.Run("When not requesting the rendered view the lifecycle overlay should not be applied", func(t *testing.T) {
		apps := []domain.ApplicationProviderSpec{newTestRenderedContainerApp(t, "app-1")}
		d := newTestRenderedDevice(t, map[string]string{
			domain.DeviceAnnotationRenderedVersion:      "1",
			domain.DeviceAnnotationApplicationLifecycle: `{"app-1":{"desiredState":"stopped"}}`,
		}, apps)

		resource, err := d.ToApiResource()
		require.NoError(t, err)
		assert.Nil(t, resource.Spec.Applications, "non-rendered view should not populate applications from RenderedApplications")
	})
}
